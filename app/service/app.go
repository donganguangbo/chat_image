package service

import (
	"context"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func Run(registerRoutes func(*http.ServeMux, *Server, string, string)) {
	rand.Seed(time.Now().UnixNano())

	configDir := env("WORKFLOW_CONFIG_DIR", "./app/configs/workflows")
	serviceConfigFile := env("SERVICE_CONFIG_FILE", "./app/configs/service.yaml")
	comfyURLs := parseComfyURLs()
	listenAddr := env("LISTEN_ADDR", ":8090")
	outputDir := env("OUTPUT_DIR", "./outputs")

	// HMAC shared secret is required. Without it every request is rejected as
	// unauthorized — no silent fall-through to an unprotected gateway. Generate one
	// with `openssl rand -hex 32` and share it between gateway and chat-web.
	gatewaySecret := strings.TrimSpace(env("GATEWAY_SECRET", ""))
	if gatewaySecret == "" {
		log.Fatal("GATEWAY_SECRET is required (set a strong random string, e.g. `openssl rand -hex 32`)")
	}

	absConfigDir, _ := filepath.Abs(configDir)
	absServiceConfigFile, _ := filepath.Abs(serviceConfigFile)
	absOutputDir, _ := filepath.Abs(outputDir)

	workflows, err := loadWorkflows(absConfigDir)
	if err != nil {
		log.Fatalf("load workflows failed: %v", err)
	}
	log.Printf("loaded workflows: %d", len(workflows))
	for _, cfg := range workflows {
		log.Printf("- %s -> %s", cfg.ID, cfg.TemplatePath)
	}
	serviceCfg, err := loadServiceConfig(absServiceConfigFile, workflows)
	if err != nil {
		log.Fatalf("load service config failed: %v", err)
	}
	log.Printf(
		"category defaults: t2i=%s edit=%s t2v=%s i2v=%s",
		serviceCfg.DefaultWorkflows.TextToImage,
		serviceCfg.DefaultWorkflows.ImageEdit,
		serviceCfg.DefaultWorkflows.TextToVideo,
		serviceCfg.DefaultWorkflows.ImageToVideo,
	)
	log.Printf("expose internal: workflows=%v raw_task_create=%v", serviceCfg.ExposeWorkflowCatalog, serviceCfg.ExposeRawTaskCreate)

	comfyPool := NewComfyPool(comfyURLs)
	if len(comfyPool.URLs()) == 0 {
		log.Fatal("no comfy backends configured (set COMFY_URL or COMFY_URLS)")
	}
	workerCount := envInt("GO_WORKERS", len(comfyPool.URLs()))
	// S3 credentials come from the environment (S3_ACCESS_KEY / S3_SECRET_KEY),
	// never from service.yaml, so secrets stay out of the repo. Endpoint/region/
	// bucket live in service.yaml but env vars may override them too.
	s3cfg := serviceCfg.S3
	if v := env("S3_ENDPOINT", ""); v != "" {
		s3cfg.Endpoint = v
	}
	if v := env("S3_REGION", ""); v != "" {
		s3cfg.Region = v
	}
	if v := env("S3_BUCKET", ""); v != "" {
		s3cfg.Bucket = v
	}
	if v := env("S3_ACCESS_KEY", ""); v != "" {
		s3cfg.AccessKey = v
	}
	if v := env("S3_SECRET_KEY", ""); v != "" {
		s3cfg.SecretKey = v
	}
	var storage *ObjectStorage
	if serviceCfg.StorageMode == StorageModeS3 {
		var s3Enabled bool
		storage, s3Enabled, err = NewObjectStorageFromConfig(s3cfg)
		if err != nil {
			log.Fatalf("object storage misconfigured: %v", err)
		}
		if !s3Enabled || storage == nil {
			log.Fatal("storage_mode=s3 requires complete s3 config")
		}
		log.Printf("[storage] Linode object storage enabled (bucket=%s)", s3cfg.Bucket)
	} else {
		if err := os.MkdirAll(filepath.Join(absOutputDir, "images"), 0o755); err != nil {
			log.Fatalf("mkdir outputs/images failed: %v", err)
		}
		if err := os.MkdirAll(filepath.Join(absOutputDir, "videos"), 0o755); err != nil {
			log.Fatalf("mkdir outputs/videos failed: %v", err)
		}
		log.Printf("[storage] local output storage enabled")
	}

	// One-shot outputs migration: MIGRATE_OUTPUTS=dry lists keys, =1 uploads, then
	// the process exits without serving. Safe to run beside a live gateway — no DB,
	// no disk writes, no port bind. Keys mirror the chat-side backfill, so a later
	// DB-ref backfill finds the objects already present and only rewrites refs.
	if mode := strings.TrimSpace(env("MIGRATE_OUTPUTS", "")); mode != "" {
		if serviceCfg.StorageMode != StorageModeS3 || storage == nil {
			log.Fatal("MIGRATE_OUTPUTS requires storage_mode=s3 and complete s3 config")
		}
		dry := strings.EqualFold(mode, "dry")
		log.Printf("[migrate] starting outputs migration (dir=%s dry=%v)", absOutputDir, dry)
		if err := migrateOutputsToS3(context.Background(), storage, absOutputDir, dry); err != nil {
			log.Fatalf("[migrate] %v", err)
		}
		return
	}

	srv := NewServer(workflows, comfyPool, serviceCfg, workerCount, absOutputDir, storage)

	mux := http.NewServeMux()
	registerRoutes(mux, srv, absOutputDir, serviceCfg.StorageMode)

	log.Printf("go gateway listening on %s", listenAddr)
	log.Printf("comfy backends: %s", strings.Join(comfyPool.URLs(), ", "))
	log.Printf("go workers: %d", workerCount)
	log.Printf("config dir: %s", absConfigDir)
	log.Printf("service config: %s", absServiceConfigFile)
	if serviceCfg.StorageMode == StorageModeLocal {
		log.Printf("output dir: %s", absOutputDir)
	} else {
		log.Printf("output dir: disabled for final files (storage_mode=s3 uses temporary files)")
	}
	log.Printf("hmac auth: enabled (window=%ds, /healthz and /outputs bypass)", hmacTimeWindowSec)
	nonces := newNonceCache()
	handler := withHMACAuth(withCORS(mux), gatewaySecret, nonces)
	httpSrv := &http.Server{
		Addr:              listenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := httpSrv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func parseComfyURLs() []string {
	primary := strings.TrimSpace(os.Getenv("COMFY_URL"))
	all := strings.TrimSpace(os.Getenv("COMFY_URLS"))
	if all == "" {
		if primary == "" {
			primary = "http://127.0.0.1:8188"
		}
		return []string{primary}
	}
	parts := strings.FieldsFunc(all, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	out := make([]string, 0, len(parts)+1)
	seen := map[string]struct{}{}
	if primary != "" {
		normalized := strings.TrimRight(primary, "/")
		out = append(out, normalized)
		seen[normalized] = struct{}{}
	}
	for _, p := range parts {
		u := strings.TrimRight(strings.TrimSpace(p), "/")
		if u == "" {
			continue
		}
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	return out
}

func env(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

func envInt(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func envFloat(key string, fallback float64) float64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		return fallback
	}
	return f
}
