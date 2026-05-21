package internal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/steps"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
	"github.com/gorilla/websocket"
)

const maxLogCaptureBytes = 10 * 1024 * 1024

func (p *DOProvider) CaptureLogs(ctx context.Context, req interfaces.LogCaptureRequest, sink interfaces.LogCaptureSink) error {
	if sink == nil {
		sink = &discardLogSink{}
	}
	client := p.AppsLogClient()
	if client == nil {
		return fmt.Errorf("digitalocean CaptureLogs: provider not initialized")
	}
	boundedFollow := req.Follow && req.DurationSeconds > 0
	if boundedFollow {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(req.DurationSeconds)*time.Second)
		defer cancel()
	}
	if req.ResourceName == "" {
		return fmt.Errorf("digitalocean CaptureLogs: resource_name is required")
	}
	if req.ResourceType != "" && req.ResourceType != "infra.container_service" {
		return fmt.Errorf("digitalocean CaptureLogs: unsupported resource_type %q", req.ResourceType)
	}
	appID := req.ProviderID
	if appID == "" {
		var err error
		appID, err = resolveLogCaptureAppID(ctx, client, req.ResourceName)
		if err != nil {
			return fmt.Errorf("digitalocean CaptureLogs: %w", err)
		}
	}
	logType, err := parseCaptureLogType(req.LogType)
	if err != nil {
		return err
	}
	tailLines := req.TailLines
	if tailLines <= 0 {
		tailLines = 300
	}
	appLogs, _, err := client.GetLogs(ctx, appID, req.DeploymentID, req.ComponentName, logType, req.Follow, tailLines)
	if err != nil {
		return fmt.Errorf("digitalocean CaptureLogs: GetLogs app=%s: %w", req.ResourceName, err)
	}
	if appLogs != nil && len(appLogs.HistoricURLs) > 0 {
		data, err := fetchCaptureLogContent(ctx, appLogs.HistoricURLs[0], tailLines)
		if err != nil {
			return err
		}
		if err := sink.WriteLogChunk(interfaces.LogChunk{Data: data, Source: "historic"}); err != nil {
			return err
		}
	}
	if req.Follow && appLogs != nil && appLogs.LiveURL != "" {
		return streamCaptureLiveURLWithLimit(ctx, appLogs.LiveURL, sink, maxLogCaptureBytes, boundedFollow)
	}
	return nil
}

func resolveLogCaptureAppID(ctx context.Context, client steps.IaCLogsClient, appName string) (string, error) {
	const perPage = 200
	for page := 1; ; page++ {
		apps, _, err := client.List(ctx, &godo.ListOptions{Page: page, PerPage: perPage})
		if err != nil {
			return "", err
		}
		for _, app := range apps {
			if app.Spec != nil && app.Spec.Name == appName {
				return app.ID, nil
			}
		}
		if len(apps) < perPage {
			break
		}
	}
	return "", fmt.Errorf("app %q not found in DigitalOcean App Platform", appName)
}

func parseCaptureLogType(s string) (godo.AppLogType, error) {
	switch strings.ToUpper(s) {
	case "", "RUN":
		return godo.AppLogTypeRun, nil
	case "BUILD":
		return godo.AppLogTypeBuild, nil
	case "DEPLOY":
		return godo.AppLogTypeDeploy, nil
	case "RUN_RESTARTED":
		return godo.AppLogTypeRunRestarted, nil
	default:
		return "", fmt.Errorf("digitalocean CaptureLogs: unsupported log_type %q", s)
	}
}

func fetchCaptureLogContent(ctx context.Context, rawURL string, tailLines int) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("digitalocean CaptureLogs: invalid historic log URL")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("digitalocean CaptureLogs: fetch historic logs: %s", redactCaptureURLError(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("digitalocean CaptureLogs: fetch historic logs: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxLogCaptureBytes))
	if err != nil {
		return nil, fmt.Errorf("digitalocean CaptureLogs: read historic logs: %w", err)
	}
	return []byte(tailCaptureString(string(body), tailLines)), nil
}

func streamCaptureLiveURL(ctx context.Context, liveURL string, sink interfaces.LogCaptureSink) error {
	return streamCaptureLiveURLWithLimit(ctx, liveURL, sink, maxLogCaptureBytes, false)
}

func streamCaptureLiveURLWithLimit(ctx context.Context, liveURL string, sink interfaces.LogCaptureSink, readLimit int64, allowAbnormalCloseAfterData bool) error {
	liveURL, err := normalizeCaptureLiveURL(liveURL)
	if err != nil {
		return err
	}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, liveURL, nil)
	if err != nil {
		return fmt.Errorf("digitalocean CaptureLogs: connect live logs: %s", redactCaptureURLError(err))
	}
	if readLimit > 0 {
		conn.SetReadLimit(readLimit)
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	defer func() {
		close(done)
		_ = conn.Close()
	}()
	sawChunk := false
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return nil
			}
			if allowAbnormalCloseAfterData && sawChunk && websocket.IsCloseError(err, websocket.CloseAbnormalClosure) {
				return nil
			}
			return fmt.Errorf("digitalocean CaptureLogs: read live logs: %s", redactCaptureURLError(err))
		}
		if len(data) == 0 {
			continue
		}
		if err := sink.WriteLogChunk(interfaces.LogChunk{Data: data, Source: "live"}); err != nil {
			return err
		}
		sawChunk = true
	}
}

func normalizeCaptureLiveURL(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("digitalocean CaptureLogs: invalid live log URL")
	}
	switch u.Scheme {
	case "ws", "wss":
		return u.String(), nil
	case "http":
		u.Scheme = "ws"
		return u.String(), nil
	case "https":
		u.Scheme = "wss"
		return u.String(), nil
	default:
		return "", fmt.Errorf("digitalocean CaptureLogs: unsupported live log URL scheme %q", u.Scheme)
	}
}

func redactCaptureURLError(err error) string {
	if err == nil {
		return ""
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Sprintf("%s <redacted-url>: %v", ue.Op, ue.Err)
	}
	return err.Error()
}

func tailCaptureString(s string, n int) string {
	if n <= 0 {
		return s
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n") + "\n"
}
