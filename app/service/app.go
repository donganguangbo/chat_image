package service

import (
	"context"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func Run(registerRoutes func(*http.ServeMux, *Server, string, string)) {
	rand.Seed(time.Now().UnixNano())

	configDir := env("WORKFLOW_CONFIG_DIR", "./app/configs/workflows")
	serviceConfigFile := env("SERVICE_CONFIG_FILE", "./app/configs/service.yaml")
	comfyURLs := parseComfyURLs()
	listenAddr := env("LISTEN_ADDR", ":8090")
	outputDir := env("OUTPUT_DIR", "./outputs")

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
	// HMAC shared secret is required. Env still wins, but service.yaml may carry
	// the deploy-time value so scripts don't have to pass it every run.
	gatewaySecret := strings.TrimSpace(serviceCfg.GatewaySecret)
	if v := strings.TrimSpace(env("GATEWAY_SECRET", "")); v != "" {
		gatewaySecret = v
	}
	if gatewaySecret == "" {
		log.Fatal("gateway_secret is required in service config or GATEWAY_SECRET env")
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
	// S3 config may live in service.yaml; env vars override it for deploys that
	// keep secrets outside the repo.
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
	if v := env("TEXT_BASE_URL", ""); v != "" {
		serviceCfg.Text.BaseURL = v
	}
	if v := env("TEXT_MODEL", ""); v != "" {
		serviceCfg.Text.Model = v
	}
	if v := env("TEXT_API_KEY", ""); v != "" {
		serviceCfg.Text.APIKey = v
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
	if strings.TrimSpace(serviceCfg.Text.BaseURL) != "" {
		log.Printf("[text] backend enabled (base_url=%s model=%s)", serviceCfg.Text.BaseURL, serviceCfg.Text.Model)
	} else {
		log.Printf("[text] backend disabled")
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

	// Sweep temp files orphaned by a previous hard crash (SIGKILL/OOM before the
	// deferred cleanups ran). Frequent restarts would otherwise accumulate these.
	cleanupStaleTempFiles(time.Hour)

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
	log.Printf("hmac auth: enabled (window=%ds, /healthz and /readyz bypass)", hmacTimeWindowSec)
	nonces := newNonceCache()
	// Cap total concurrent pre-auth request-body buffering (bytes, not count) so
	// neither huge bodies nor a flood of small ones can OOM the gateway. ~7 legit
	// ~134 MB i2v uploads fit in the 1 GiB default.
	bodyBudget := newByteBudget(int64(envInt("GATEWAY_MAX_INFLIGHT_BODY_BYTES", 1<<30)))
	handler := withHMACAuth(withCORS(mux), gatewaySecret, nonces, bodyBudget)
	httpSrv := &http.Server{
		Addr:              listenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Serve in the background so the main goroutine can wait for a shutdown
	// signal and drain gracefully (fail in-flight tasks, interrupt orphan Comfy
	// jobs) instead of the process being killed mid-flight and leaving clients
	// polling tasks that no longer exist.
	serveErr := make(chan error, 1)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serveErr <- err
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-serveErr:
		log.Fatal(err)
	case sig := <-sigCh:
		log.Printf("[shutdown] received %s, draining...", sig)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			log.Printf("[shutdown] http server shutdown error: %v", err)
		}
		// Fresh deadline for orphan interrupts: httpSrv.Shutdown may have burned
		// the whole 20s draining a long in-flight request, which would leave the
		// Interrupt() calls below running on an already-expired context.
		interruptCtx, cancelInt := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelInt()
		srv.Shutdown(interruptCtx)
		log.Printf("[shutdown] done")
	}
}

// cleanupStaleTempFiles removes leftover gateway temp files/dirs older than
// maxAge from the system temp dir. These are normally cleaned up on their own
// (defer os.Remove / RemoveAll on every path); only a hard crash before those
// run leaks them. The age gate avoids racing a concurrently-starting instance.
func cleanupStaleTempFiles(maxAge time.Duration) {
	tmp := os.TempDir()
	prefixes := []string{
		"go_gateway_img_*",
		"go_gateway_img_b64_*",
		"go_gateway_avif_*",
		"go_gateway_h264_video_*",
		"chat-image-output-*",
	}
	cutoff := time.Now().Add(-maxAge)
	removed := 0
	for _, pat := range prefixes {
		matches, _ := filepath.Glob(filepath.Join(tmp, pat))
		for _, m := range matches {
			fi, err := os.Stat(m)
			if err != nil || fi.ModTime().After(cutoff) {
				continue
			}
			if err := os.RemoveAll(m); err == nil {
				removed++
			}
		}
	}
	if removed > 0 {
		log.Printf("[startup] swept %d stale temp file(s) from %s", removed, tmp)
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
