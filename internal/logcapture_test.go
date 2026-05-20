package internal

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/GoCodeAlone/workflow/interfaces"
	pb "github.com/GoCodeAlone/workflow/plugin/external/proto"
	"github.com/digitalocean/godo"
	"github.com/gorilla/websocket"
)

type captureSink struct {
	bytes.Buffer
}

func (s *captureSink) WriteLogChunk(chunk interfaces.LogChunk) error {
	_, err := s.Write(chunk.Data)
	return err
}

func TestDOProviderCaptureLogsFetchesHistoricLogs(t *testing.T) {
	logSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("old\nnew\n"))
	}))
	t.Cleanup(logSrv.Close)

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/apps":
			_, _ = w.Write([]byte(`{"apps":[{"id":"app-123","spec":{"name":"bmw-staging"}}]}`))
		case r.URL.Path == "/v2/apps/app-123/logs":
			if r.URL.Query().Get("type") != "RUN" {
				t.Fatalf("type query = %q, want RUN", r.URL.Query().Get("type"))
			}
			if r.URL.Query().Get("component_name") != "api" {
				t.Fatalf("component_name = %q, want api", r.URL.Query().Get("component_name"))
			}
			_, _ = w.Write([]byte(`{"historic_urls":["` + logSrv.URL + `"]}`))
		default:
			t.Fatalf("unexpected API path: %s", r.URL.String())
		}
	}))
	t.Cleanup(apiSrv.Close)

	p := NewDOProvider()
	if err := p.Initialize(context.Background(), map[string]any{"token": "test-token"}); err != nil {
		t.Fatal(err)
	}
	p.client.BaseURL = mustURL(t, apiSrv.URL+"/v2/")

	var sink captureSink
	err := p.CaptureLogs(context.Background(), interfaces.LogCaptureRequest{
		ResourceName:  "bmw-staging",
		ResourceType:  "infra.container_service",
		ComponentName: "api",
		LogType:       "RUN",
		TailLines:     20,
	}, &sink)
	if err != nil {
		t.Fatalf("CaptureLogs: %v", err)
	}
	if sink.String() != "old\nnew\n" {
		t.Fatalf("captured logs = %q", sink.String())
	}
}

func TestDOIaCServerCaptureLogsStreamsChunks(t *testing.T) {
	logSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("one\ntwo\n"))
	}))
	t.Cleanup(logSrv.Close)

	provider := NewDOProvider()
	provider.client = godo.NewFromToken("test-token")
	server := newDOIaCServer(provider)
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/apps":
			_, _ = w.Write([]byte(`{"apps":[{"id":"app-123","spec":{"name":"bmw-staging"}}]}`))
		case strings.HasPrefix(r.URL.Path, "/v2/apps/app-123/logs"):
			_, _ = w.Write([]byte(`{"historic_urls":["` + logSrv.URL + `"]}`))
		default:
			t.Fatalf("unexpected API path: %s", r.URL.String())
		}
	}))
	t.Cleanup(apiSrv.Close)
	provider.client.BaseURL = mustURL(t, apiSrv.URL+"/v2/")

	stream := &fakeLogCaptureStream{}
	err := server.CaptureLogs(&pb.CaptureLogsRequest{
		ResourceName:  "bmw-staging",
		ResourceType:  "infra.container_service",
		LogType:       pb.LogCaptureType_LOG_CAPTURE_TYPE_RUN,
		ComponentName: "api",
		TailLines:     10,
	}, stream)
	if err != nil {
		t.Fatalf("CaptureLogs RPC: %v", err)
	}
	if got := string(stream.buf.Bytes()); got != "one\ntwo\n" {
		t.Fatalf("streamed logs = %q", got)
	}
}

func TestFetchCaptureLogContentDoesNotLeakSignedURLOnFailure(t *testing.T) {
	signedURL := "http://127.0.0.1:1/logs?X-Amz-Signature=secret-token"
	_, err := fetchCaptureLogContent(context.Background(), signedURL, 20)
	if err == nil {
		t.Fatal("expected fetch error")
	}
	msg := err.Error()
	if strings.Contains(msg, "secret-token") || strings.Contains(msg, signedURL) || strings.Contains(msg, "X-Amz-Signature") {
		t.Fatalf("error leaked signed URL material: %q", msg)
	}
	if !strings.Contains(msg, "<redacted-url>") {
		t.Fatalf("error = %q, want redacted URL marker", msg)
	}
}

func TestStreamCaptureLiveURLStreamsMessage(t *testing.T) {
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		if err := conn.WriteMessage(websocket.TextMessage, []byte("live line\n")); err != nil {
			t.Errorf("write websocket message: %v", err)
		}
		if err := conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")); err != nil {
			t.Errorf("write websocket close: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	var sink captureSink
	err := streamCaptureLiveURL(context.Background(), wsURL(srv.URL), &sink)
	if err != nil {
		t.Fatalf("streamCaptureLiveURL: %v", err)
	}
	if got := sink.String(); got != "live line\n" {
		t.Fatalf("captured live logs = %q", got)
	}
}

func TestStreamCaptureLiveURLRejectsOversizedMessage(t *testing.T) {
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		_ = conn.WriteMessage(websocket.TextMessage, bytes.Repeat([]byte("x"), 64))
	}))
	t.Cleanup(srv.Close)

	var sink captureSink
	err := streamCaptureLiveURLWithLimit(context.Background(), wsURL(srv.URL), &sink, 8)
	if err == nil {
		t.Fatal("expected oversized live log frame error")
	}
	if !strings.Contains(err.Error(), "read live logs") {
		t.Fatalf("error = %q, want read live logs context", err)
	}
	if sink.Len() != 0 {
		t.Fatalf("captured oversized live logs: %q", sink.String())
	}
}

func TestStreamCaptureLiveURLExitsOnContextCancel(t *testing.T) {
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := streamCaptureLiveURL(ctx, wsURL(srv.URL), &captureSink{})
	if err != nil {
		t.Fatalf("streamCaptureLiveURL: %v", err)
	}
}

type fakeLogCaptureStream struct {
	pb.IaCProviderLogCapture_CaptureLogsServer
	buf bytes.Buffer
}

func (s *fakeLogCaptureStream) Context() context.Context {
	return context.Background()
}

func (s *fakeLogCaptureStream) Send(chunk *pb.LogChunk) error {
	_, err := s.buf.Write(chunk.GetData())
	return err
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func wsURL(raw string) string {
	return "ws" + strings.TrimPrefix(raw, "http")
}
