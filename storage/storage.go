package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"chat_image/model"

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
	client *s3.Client
	bucket string
}

type storageConfig struct {
	endpoint  string
	region    string
	bucket    string
	accessKey string
	secretKey string
}

// validateStorageConfig trims the five settings and decides whether object
// storage is enabled. ok=false with no error means bucket is empty (disabled →
// disk-only); an error means bucket is set but a companion value is missing.
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
	return &ObjectStorage{client: client, bucket: cfg.bucket}, nil
}

// NewObjectStorageFromConfig builds an ObjectStorage from the service.yaml `s3:`
// section. Returns (nil, false, nil) when the bucket is unset (disabled →
// disk-only).
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
