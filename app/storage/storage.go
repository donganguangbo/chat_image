package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"chat_image/app/model"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// ObjectStorage uploads generated media to a Linode (S3-compatible) bucket.
// Client construction mirrors go-pay/bucket_test.go: path-style addressing,
// a custom endpoint, and request-checksum opt-out (Linode rejects the SDK's
// default aws-chunked checksums).
type ObjectStorage struct {
	client  *s3.Client
	presign *s3.PresignClient
	bucket  string
}

type storageConfig struct {
	endpoint  string
	region    string
	bucket    string
	accessKey string
	secretKey string
}

// validateStorageConfig trims the five settings and decides whether object
// storage can be built. ok=false with no error means no bucket was configured.
func validateStorageConfig(endpoint, region, bucket, accessKey, secretKey string) (storageConfig, bool, error) {
	c := storageConfig{
		endpoint:  strings.TrimSpace(endpoint),
		region:    strings.TrimSpace(region),
		bucket:    strings.TrimSpace(bucket),
		accessKey: strings.TrimSpace(accessKey),
		secretKey: strings.TrimSpace(secretKey),
	}
	if c.bucket == "" {
		return storageConfig{}, false, nil
	}
	if c.endpoint == "" || c.region == "" || c.accessKey == "" || c.secretKey == "" {
		return storageConfig{}, false, fmt.Errorf(
			"s3 config: bucket set but endpoint/region/access_key/secret_key incomplete")
	}
	return c, true, nil
}

// storageConfigFromEnv reads S3_* via the given getenv (kept for tests/env use).
func storageConfigFromEnv(getenv func(string) string) (storageConfig, bool, error) {
	return validateStorageConfig(
		getenv("S3_ENDPOINT"), getenv("S3_REGION"), getenv("S3_BUCKET"),
		getenv("S3_ACCESS_KEY"), getenv("S3_SECRET_KEY"))
}

// buildObjectStorage constructs the Linode S3 client (path-style + custom
// endpoint + request/response checksum opt-out, mirroring go-pay/bucket_test.go).
func buildObjectStorage(cfg storageConfig) (*ObjectStorage, error) {
	awsCfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion(cfg.region),
		config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.accessKey, cfg.secretKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("load s3 config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String("https://" + cfg.endpoint)
		o.UsePathStyle = true
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	})
	return &ObjectStorage{client: client, presign: s3.NewPresignClient(client), bucket: cfg.bucket}, nil
}

// NewObjectStorageFromConfig builds an ObjectStorage from the service.yaml `s3:`
// section. Returns (nil, false, nil) when the bucket is unset.
func NewObjectStorageFromConfig(c model.S3FileConfig) (*ObjectStorage, bool, error) {
	cfg, ok, err := validateStorageConfig(c.Endpoint, c.Region, c.Bucket, c.AccessKey, c.SecretKey)
	if err != nil || !ok {
		return nil, false, err
	}
	st, err := buildObjectStorage(cfg)
	if err != nil {
		return nil, false, err
	}
	return st, true, nil
}

// gatewayObjectKey builds the S3 key for a generated file. MUST stay consistent
// with the chat-side scheme in
// services/chat-web/src/server/lib/object-storage-keys.ts:
//
//	images/{yyyymmdd}/{name}   for images
//	videos/{yyyymmdd}/{name}   for videos
func ObjectKey(kind, name string, now time.Time) string {
	if kind == "video" {
		return fmt.Sprintf("videos/%s/%s", now.UTC().Format("20060102"), name)
	}
	return fmt.Sprintf("images/%s/%s", now.UTC().Format("20060102"), name)
}

// UploadPrivate stores body at key. The bucket is private by default and we send
// NO x-amz-acl header — Linode rejects ACL headers on buckets with object ACLs
// disabled (and the key may lack PutObjectAcl). Mirrors go-pay/bucket_test.go.
// body should be a seekable reader (e.g. *os.File or *bytes.Reader) so the SDK
// can compute the payload length/signature without buffering it all in memory.
func (o *ObjectStorage) UploadPrivate(ctx context.Context, key string, body io.Reader, contentType string) error {
	in := &s3.PutObjectInput{
		Bucket: aws.String(o.bucket),
		Key:    aws.String(key),
		Body:   body,
	}
	if contentType != "" {
		in.ContentType = aws.String(contentType)
	}
	if _, err := o.client.PutObject(ctx, in); err != nil {
		return fmt.Errorf("upload %s to %s: %w", key, o.bucket, err)
	}
	return nil
}

func (o *ObjectStorage) PresignGetObject(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	out, err := o.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(o.bucket),
		Key:    aws.String(key),
	}, func(opts *s3.PresignOptions) {
		opts.Expires = ttl
	})
	if err != nil {
		return "", fmt.Errorf("presign %s from %s: %w", key, o.bucket, err)
	}
	return out.URL, nil
}

// Exists reports whether key is already present in the bucket. A 404 (NotFound)
// is reported as (false, nil); any other error is surfaced. Used by the one-shot
// outputs migration so re-runs are idempotent and resumable.
func (o *ObjectStorage) Exists(ctx context.Context, key string) (bool, error) {
	_, err := o.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(o.bucket),
		Key:    aws.String(key),
	})
	if err == nil {
		return true, nil
	}
	var nf *types.NotFound
	if errors.As(err, &nf) {
		return false, nil
	}
	var re *awshttp.ResponseError
	if errors.As(err, &re) && re.HTTPStatusCode() == 404 {
		return false, nil
	}
	return false, err
}

// ListObjects returns every object key under prefix (following pagination).
func (o *ObjectStorage) ListObjects(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	p := s3.NewListObjectsV2Paginator(o.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(o.bucket),
		Prefix: aws.String(prefix),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list %q under %s: %w", prefix, o.bucket, err)
		}
		for _, obj := range page.Contents {
			if obj.Key != nil {
				keys = append(keys, *obj.Key)
			}
		}
	}
	return keys, nil
}

// Download returns the full object body for key.
func (o *ObjectStorage) Download(ctx context.Context, key string) ([]byte, error) {
	out, err := o.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(o.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("download %s from %s: %w", key, o.bucket, err)
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}
