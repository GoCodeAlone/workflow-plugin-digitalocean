package internal

import (
	"context"
	"errors"
	"fmt"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Sentinel errors for config-validation failures in BootstrapStateBackend.
var (
	errMissingBucket      = errors.New("missing required config key 'bucket'")
	errMissingCredentials = errors.New("missing required config keys 'access_key' and/or 'secret_key'")
)

// spacesBucketClient abstracts the S3 operations needed for state bucket
// bootstrapping. It is satisfied by *s3.Client and can be faked in tests.
type spacesBucketClient interface {
	HeadBucket(ctx context.Context, input *s3.HeadBucketInput, opts ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
	CreateBucket(ctx context.Context, input *s3.CreateBucketInput, opts ...func(*s3.Options)) (*s3.CreateBucketOutput, error)
}

// newSpacesS3Client builds an S3 client pointed at the DO Spaces endpoint for
// the given region. Construction mirrors drivers/spaces.go s3SpacesClient.s3Client.
func newSpacesS3Client(accessKey, secretKey, region string) spacesBucketClient {
	endpoint := fmt.Sprintf("https://%s.digitaloceanspaces.com", region)
	return s3.New(s3.Options{
		Region:       region,
		BaseEndpoint: aws.String(endpoint),
		Credentials:  aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	})
}

// BootstrapStateBackend ensures the DO Spaces state bucket exists. It is
// idempotent: if the bucket already exists it returns the metadata without
// error. Providers that do not manage a state backend should return (nil, nil).
//
// Required cfg keys: "bucket", and credentials supplied as either camelCase
// ("accessKey"/"secretKey", e.g. BMW infra.yaml) or snake_case
// ("access_key"/"secret_key"); camelCase is checked first.
// Optional cfg keys: "region" (falls back to the provider's configured region,
// then "nyc3" as the ultimate default).
//
// Returns a BootstrapResult with:
//   - Bucket, Region, Endpoint fields populated
//   - EnvVars: WFCTL_STATE_BUCKET and SPACES_BUCKET set to the bucket name
func (p *DOProvider) BootstrapStateBackend(ctx context.Context, cfg map[string]any) (*interfaces.BootstrapResult, error) {
	return p.bootstrapStateBackendWithFactory(ctx, cfg, newSpacesS3Client)
}

// bootstrapStateBackendWithFactory is the testable core of BootstrapStateBackend.
// clientFactory receives the resolved credentials and region and returns the
// client used for HeadBucket/CreateBucket, allowing tests to inject a
// fakeBucketClient without any network I/O.
func (p *DOProvider) bootstrapStateBackendWithFactory(
	ctx context.Context,
	cfg map[string]any,
	clientFactory func(accessKey, secretKey, region string) spacesBucketClient,
) (*interfaces.BootstrapResult, error) {
	bucket, _ := cfg["bucket"].(string)
	if bucket == "" {
		return nil, fmt.Errorf("digitalocean bootstrap: %w", errMissingBucket)
	}

	region, _ := cfg["region"].(string)
	if region == "" {
		region = p.region // provider-level default (set during Initialize)
	}
	if region == "" {
		region = "nyc3"
	}

	accessKey, _ := cfg["accessKey"].(string) // camelCase (BMW infra.yaml)
	if accessKey == "" {
		accessKey, _ = cfg["access_key"].(string) // snake_case fallback
	}
	secretKey, _ := cfg["secretKey"].(string)
	if secretKey == "" {
		secretKey, _ = cfg["secret_key"].(string)
	}
	if accessKey == "" || secretKey == "" {
		return nil, fmt.Errorf("digitalocean bootstrap: %w", errMissingCredentials)
	}

	endpoint := fmt.Sprintf("https://%s.digitaloceanspaces.com", region)
	client := clientFactory(accessKey, secretKey, region)

	if err := bootstrapSpacesBucketWithClient(ctx, bucket, region, client); err != nil {
		return nil, fmt.Errorf("digitalocean bootstrap: %w", err)
	}

	return &interfaces.BootstrapResult{
		Bucket:   bucket,
		Region:   region,
		Endpoint: endpoint,
		EnvVars: map[string]string{
			"WFCTL_STATE_BUCKET": bucket,
			"SPACES_BUCKET":      bucket,
		},
	}, nil
}

// bootstrapSpacesBucketWithClient is the testable core of the S3 round-trip.
// It uses HeadBucket to check existence and CreateBucket to create if absent.
func bootstrapSpacesBucketWithClient(ctx context.Context, bucket, region string, client spacesBucketClient) error {
	_, headErr := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
	if headErr == nil {
		fmt.Printf("  state backend: bucket %q already exists — skipped\n", bucket)
		return nil
	}

	// HeadBucket returns types.NotFound (HTTP 404) when the bucket does not exist.
	// Any other error (e.g. 403 Forbidden — owned by another account) is fatal.
	var notFound *s3types.NotFound
	if !errors.As(headErr, &notFound) {
		return fmt.Errorf("check bucket %q: %w", bucket, headErr)
	}

	_, createErr := client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
		CreateBucketConfiguration: &s3types.CreateBucketConfiguration{
			LocationConstraint: s3types.BucketLocationConstraint(region),
		},
	})
	if createErr != nil {
		// BucketAlreadyOwnedByYou: a concurrent create won the race — still OK.
		var alreadyOwned *s3types.BucketAlreadyOwnedByYou
		if errors.As(createErr, &alreadyOwned) {
			fmt.Printf("  state backend: bucket %q already owned — skipped\n", bucket)
			return nil
		}
		return fmt.Errorf("create bucket %q: %w", bucket, createErr)
	}
	fmt.Printf("  state backend: created spaces state bucket %q in %s\n", bucket, region)
	return nil
}
