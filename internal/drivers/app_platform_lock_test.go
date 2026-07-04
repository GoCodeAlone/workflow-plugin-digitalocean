package drivers

import (
	"context"
	"testing"
	"time"

	"github.com/digitalocean/godo"
)

type blockingDeploymentListClient struct {
	internalMockAppClient
	entered chan struct{}
	release chan struct{}
}

func newBlockingDeploymentListClient() *blockingDeploymentListClient {
	return &blockingDeploymentListClient{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (c *blockingDeploymentListClient) ListDeployments(_ context.Context, _ string, _ *godo.ListOptions) ([]*godo.Deployment, *godo.Response, error) {
	close(c.entered)
	<-c.release
	return nil, &godo.Response{}, nil
}

func TestAppPlatformDeploymentListDoesNotBlockDeploymentStateReads(t *testing.T) {
	client := newBlockingDeploymentListClient()
	driver := NewAppPlatformDriverWithClient(client, "nyc3")
	driver.setUpdateDeploymentState("app-1", appDeploymentWaitState{previousActiveDeploymentID: "old"})

	errCh := make(chan error, 1)
	go func() {
		_, _, err := driver.currentTargetDeployment(context.Background(), "app-1", &godo.App{})
		errCh <- err
	}()

	select {
	case <-client.entered:
	case <-time.After(time.Second):
		close(client.release)
		t.Fatal("currentTargetDeployment did not reach ListDeployments")
	}

	readDone := make(chan bool, 1)
	go func() {
		readDone <- driver.hasUpdateDeploymentState("app-1")
	}()

	select {
	case ok := <-readDone:
		if !ok {
			t.Fatal("deployment state unexpectedly missing")
		}
	case <-time.After(100 * time.Millisecond):
		close(client.release)
		t.Fatal("deployment state read blocked behind ListDeployments")
	}

	close(client.release)
	if err := <-errCh; err != nil {
		t.Fatalf("currentTargetDeployment: %v", err)
	}
}
