package internal

import (
	"context"
	"fmt"
	"net/url"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var _ interfaces.IaCProviderRunner = (*DOProvider)(nil)
var _ interfaces.IaCProviderJobCanceler = (*DOProvider)(nil)
var _ interfaces.IaCProviderRunner = (*doIaCServer)(nil)
var _ interfaces.IaCProviderJobCanceler = (*doIaCServer)(nil)

func (p *DOProvider) appPlatformJobRunner() (*appPlatformRunner, error) {
	if p.client == nil {
		return nil, status.Error(codes.FailedPrecondition, "digitalocean provider must be initialized before running jobs")
	}
	return newAppPlatformRunner(godoAppPlatformJobClient{AppsService: p.client.Apps, client: p.client}), nil
}

// godo exposes invocation cancellation but not the deployment cancellation
// needed before an invocation exists. Keep that typed API adapter in this provider.
type godoAppPlatformJobClient struct {
	godo.AppsService
	client *godo.Client
}

func (c godoAppPlatformJobClient) CancelDeployment(ctx context.Context, appID, deploymentID string) (*godo.Deployment, *godo.Response, error) {
	path := fmt.Sprintf("v2/apps/%s/deployments/%s/cancel", url.PathEscape(appID), url.PathEscape(deploymentID))
	req, err := c.client.NewRequest(ctx, "POST", path, nil)
	if err != nil {
		return nil, nil, err
	}
	var result struct {
		Deployment *godo.Deployment `json:"deployment"`
	}
	response, err := c.client.Do(ctx, req, &result)
	return result.Deployment, response, err
}

func (p *DOProvider) RunJob(ctx context.Context, spec interfaces.JobSpec) (*interfaces.JobHandle, error) {
	runner, err := p.appPlatformJobRunner()
	if err != nil {
		return nil, err
	}
	return runner.RunJob(ctx, spec)
}

func (p *DOProvider) JobStatus(ctx context.Context, handle interfaces.JobHandle) (*interfaces.JobStatusReply, error) {
	runner, err := p.appPlatformJobRunner()
	if err != nil {
		return nil, err
	}
	return runner.JobStatus(ctx, handle)
}

func (p *DOProvider) JobLogs(ctx context.Context, handle interfaces.JobHandle, sink interfaces.LogCaptureSink) error {
	runner, err := p.appPlatformJobRunner()
	if err != nil {
		return err
	}
	return runner.JobLogs(ctx, handle, sink)
}

func (p *DOProvider) CancelJob(ctx context.Context, handle interfaces.JobHandle) error {
	runner, err := p.appPlatformJobRunner()
	if err != nil {
		return err
	}
	return runner.CancelJob(ctx, handle)
}

func (s *doIaCServer) RunJob(ctx context.Context, spec interfaces.JobSpec) (*interfaces.JobHandle, error) {
	return s.provider.RunJob(ctx, spec)
}

func (s *doIaCServer) JobStatus(ctx context.Context, handle interfaces.JobHandle) (*interfaces.JobStatusReply, error) {
	return s.provider.JobStatus(ctx, handle)
}

func (s *doIaCServer) JobLogs(ctx context.Context, handle interfaces.JobHandle, sink interfaces.LogCaptureSink) error {
	return s.provider.JobLogs(ctx, handle, sink)
}

func (s *doIaCServer) CancelJob(ctx context.Context, handle interfaces.JobHandle) error {
	return s.provider.CancelJob(ctx, handle)
}
