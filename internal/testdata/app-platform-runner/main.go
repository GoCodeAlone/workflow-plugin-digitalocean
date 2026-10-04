package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal"
	pb "github.com/GoCodeAlone/workflow/plugin/external/proto"
	"github.com/GoCodeAlone/workflow/plugin/external/sdk"
	"golang.org/x/oauth2"
)

type fixtureTransport struct{ target *url.URL }

func (t fixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != "api.digitalocean.com" {
		return nil, fmt.Errorf("unexpected fixture API host")
	}
	copy := req.Clone(req.Context())
	copy.URL.Scheme = t.target.Scheme
	copy.URL.Host = t.target.Host
	copy.Host = ""
	return http.DefaultTransport.RoundTrip(copy)
}

func main() {
	target, err := url.Parse(os.Getenv("WORKFLOW_DO_JOB_TEST_API"))
	if err != nil || target.Scheme != "http" || (target.Hostname() != "127.0.0.1" && target.Hostname() != "localhost" && target.Hostname() != "::1") {
		panic("fixture requires a loopback API")
	}
	manifest, err := os.ReadFile(os.Getenv("WORKFLOW_DO_JOB_TEST_MANIFEST"))
	if err != nil {
		panic("fixture manifest could not be read")
	}
	server := internal.NewIaCServer()
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{Transport: fixtureTransport{target: target}})
	if _, err := server.Initialize(ctx, &pb.InitializeRequest{ConfigJson: []byte(`{"token":"fixture-token"}`)}); err != nil {
		panic("fixture provider initialization failed")
	}
	sdk.ServeIaCPlugin(server, sdk.IaCServeOptions{ManifestProvider: sdk.MustEmbedManifest(manifest), BuildVersion: sdk.ResolveBuildVersion(internal.Version)})
}
