package main

import (
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestGatewayObjectKey(t *testing.T) {
	now := time.Date(2026, 6, 9, 23, 30, 0, 0, time.UTC)
	if got := gatewayObjectKey("image", "abc_120530_out.png", now); got != "img/img/20260609/abc_120530_out.png" {
		t.Fatalf("image key = %q, want img/img/20260609/abc_120530_out.png", got)
	}
	if got := gatewayObjectKey("video", "abc_120530_out.mp4", now); got != "img/vid/20260609/abc_120530_out.mp4" {
		t.Fatalf("video key = %q, want img/vid/20260609/abc_120530_out.mp4", got)
	}
}

func TestStorageConfigFromEnv(t *testing.T) {
	if _, ok, err := storageConfigFromEnv(func(string) string { return "" }); ok || err != nil {
		t.Fatalf("empty env: ok=%v err=%v, want ok=false err=nil", ok, err)
	}
	partial := map[string]string{"S3_BUCKET": "ichat"}
	if _, ok, err := storageConfigFromEnv(func(k string) string { return partial[k] }); ok || err == nil {
		t.Fatalf("partial env: ok=%v err=%v, want ok=false err!=nil", ok, err)
	}
	full := map[string]string{
		"S3_BUCKET":     "ichat",
		"S3_ENDPOINT":   "us-ord-10.linodeobjects.com",
		"S3_REGION":     "us-ord-10",
		"S3_ACCESS_KEY": "k",
		"S3_SECRET_KEY": "s",
	}
	cfg, ok, err := storageConfigFromEnv(func(k string) string { return full[k] })
	if !ok || err != nil {
		t.Fatalf("full env: ok=%v err=%v", ok, err)
	}
	if cfg.bucket != "ichat" || cfg.region != "us-ord-10" || cfg.endpoint != "us-ord-10.linodeobjects.com" ||
		cfg.accessKey != "k" || cfg.secretKey != "s" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestServiceConfigS3YAML(t *testing.T) {
	raw := `
default_workflows:
  text_to_image: t2i
s3:
  endpoint: us-southeast-1.linodeobjects.com
  region: us-southeast-1
  bucket: ichat-e0
  access_key: AK
  secret_key: SK
`
	var cfg ServiceConfig
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.S3.Endpoint != "us-southeast-1.linodeobjects.com" || cfg.S3.Region != "us-southeast-1" ||
		cfg.S3.Bucket != "ichat-e0" || cfg.S3.AccessKey != "AK" || cfg.S3.SecretKey != "SK" {
		t.Fatalf("S3 = %+v", cfg.S3)
	}
	st, ok, err := NewObjectStorageFromConfig(cfg.S3)
	if err != nil || !ok || st == nil {
		t.Fatalf("NewObjectStorageFromConfig: ok=%v err=%v st=%v", ok, err, st)
	}
}
