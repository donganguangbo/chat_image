package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
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

// storageConfigFromEnv reads S3_* via the given getenv. Returns ok=false (no
// error) when S3_BUCKET is empty — object storage is then disabled and the
// gateway keeps writing only to local disk. Returns an error when S3_BUCKET is
// set but a required companion var is missing (misconfiguration).
func storageConfigFromEnv(getenv func(string) string) (storageConfig, bool, error) {
	c := storageConfig{
		endpoint:  strings.TrimSpace(getenv("S3_ENDPOINT")),
		region:    strings.TrimSpace(getenv("S3_REGION")),
		bucket:    strings.TrimSpace(getenv("S3_BUCKET")),
		accessKey: strings.TrimSpace(getenv("S3_ACCESS_KEY")),
		secretKey: strings.TrimSpace(getenv("S3_SECRET_KEY")),
	}
	if c.bucket == "" {
		return storageConfig{}, false, nil
	}
	if c.endpoint == "" || c.region == "" || c.accessKey == "" || c.secretKey == "" {
		return storageConfig{}, false, fmt.Errorf(
			"S3_BUCKET set but S3_ENDPOINT/S3_REGION/S3_ACCESS_KEY/S3_SECRET_KEY incomplete")
	}
	return c, true, nil
}

// NewObjectStorageFromEnv builds an ObjectStorage from S3_* env vars. When
// object storage is not configured it returns (nil, false, nil) so the caller
// can run disk-only.
func NewObjectStorageFromEnv(getenv func(string) string) (*ObjectStorage, bool, error) {
	cfg, ok, err := storageConfigFromEnv(getenv)
	if err != nil || !ok {
		return nil, false, err
	}
	awsCfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion(cfg.region),
		config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.accessKey, cfg.secretKey, "")),
	)
	if err != nil {
		return nil, false, fmt.Errorf("load s3 config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String("https://" + cfg.endpoint)
		o.UsePathStyle = true
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	})
	return &ObjectStorage{client: client, bucket: cfg.bucket}, true, nil
}

// gatewayObjectKey builds the S3 key for a generated file. MUST stay consistent
// with the chat-side scheme in
// services/chat-web/src/server/lib/object-storage-keys.ts:
//
//	img/img/{yyyymmdd}/{name}   for images
//	img/vid/{yyyymmdd}/{name}   for videos
func gatewayObjectKey(kind, name string, now time.Time) string {
	sub := "img"
	if kind == "video" {
		sub = "vid"
	}
	return fmt.Sprintf("img/%s/%s/%s", sub, now.UTC().Format("20060102"), name)
}

// UploadPrivate stores body at key with a private canned ACL.
func (o *ObjectStorage) UploadPrivate(ctx context.Context, key string, body []byte, contentType string) error {
	in := &s3.PutObjectInput{
		Bucket: aws.String(o.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(body),
		ACL:    types.ObjectCannedACLPrivate,
	}
	if contentType != "" {
		in.ContentType = aws.String(contentType)
	}
	_, err := o.client.PutObject(ctx, in)
	return err
}
