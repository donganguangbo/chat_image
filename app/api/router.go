package api

import (
	"encoding/json"
	"net/http"
	"time"

	"chat_image/app/service"
)

func Register(mux *http.ServeMux, srv *service.Server, outputDir string, storageMode string) {
	v1 := NewV1Handler(srv)
	mux.HandleFunc("/v1/images/generations", v1.HandleImageGeneration)
	mux.HandleFunc("/v1/images/edits", v1.HandleImageEdit)
	mux.HandleFunc("/v1/videos", v1.HandleVideoCreate)
	mux.HandleFunc("/v1/videos/", v1.HandleVideoByID)
	mux.HandleFunc("/v1/chat/completions", v1.HandleChatCompletion)
	mux.HandleFunc("/v1/models", v1.HandleListModels)

	if storageMode == service.StorageModeLocal {
		mux.Handle("/outputs/", http.StripPrefix("/outputs/", http.FileServer(http.Dir(outputDir))))
	}
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "time": time.Now().UTC()})
	})

	// /readyz is readiness (not liveness): it reports 503 unless at least one
	// Comfy backend answers /system_stats, so monitoring can distinguish "process
	// up" from "can actually serve generations". Point orchestration/alerts here
	// to catch run-time all-backends-down instead of the always-200 /healthz.
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		healthy, total := srv.BackendHealth()
		status := http.StatusOK
		if healthy == 0 {
			status = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":               healthy > 0,
			"backends_healthy": healthy,
			"backends_total":   total,
			"time":             time.Now().UTC(),
		})
	})
}
