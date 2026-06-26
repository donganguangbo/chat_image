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

	if storageMode == service.StorageModeLocal {
		mux.Handle("/outputs/", http.StripPrefix("/outputs/", http.FileServer(http.Dir(outputDir))))
	}
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "time": time.Now().UTC()})
	})
}
