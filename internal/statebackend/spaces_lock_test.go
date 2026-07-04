package statebackend

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type blockingPutS3Client struct {
	*mockS3Client
	entered chan struct{}
	release chan struct{}
}

func newBlockingPutS3Client() *blockingPutS3Client {
	return &blockingPutS3Client{
		mockS3Client: newMockS3Client(),
		entered:      make(chan struct{}),
		release:      make(chan struct{}),
	}
}

func (c *blockingPutS3Client) PutObject(ctx context.Context, input *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	key := aws.ToString(input.Key)
	if key == "iac-state/res-1.lock" {
		close(c.entered)
		<-c.release
	}
	return c.mockS3Client.PutObject(ctx, input, opts...)
}

func TestSpacesLockDoesNotBlockDifferentResourceLock(t *testing.T) {
	client := newBlockingPutS3Client()
	store := NewSpacesIaCStateStoreWithClient(client, "test-bucket", "iac-state/")

	errCh := make(chan error, 1)
	go func() {
		errCh <- store.Lock(context.Background(), "res-1")
	}()

	select {
	case <-client.entered:
	case <-time.After(time.Second):
		close(client.release)
		t.Fatal("first lock did not reach PutObject")
	}

	otherDone := make(chan error, 1)
	go func() {
		otherDone <- store.Lock(context.Background(), "res-2")
	}()

	select {
	case err := <-otherDone:
		if err != nil {
			t.Fatalf("second lock: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		close(client.release)
		t.Fatal("different resource lock blocked behind first lock")
	}

	close(client.release)
	if err := <-errCh; err != nil {
		t.Fatalf("first lock: %v", err)
	}
}
