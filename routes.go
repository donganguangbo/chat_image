package main

import (
	"net/http"
	"time"
)

func registerRoutes(mux *http.ServeMux, srv *Server, outputDir string) {
	mux.HandleFunc("/v1/images/generations", srv.handleOpenAIImageGeneration)
	mux.HandleFunc("/v1/images/edits", srv.handleOpenAIImageEdit)
	mux.HandleFunc("/v1/videos", srv.handleOpenAIVideoCreate)
	mux.HandleFunc("/v1/videos/", srv.handleOpenAIVideoByID)

	mux.Handle("/outputs/", http.StripPrefix("/outputs/", http.FileServer(http.Dir(outputDir))))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "time": time.Now().UTC()})
	})
}
