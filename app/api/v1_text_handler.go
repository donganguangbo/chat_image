package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"chat_image/app/service"
)

func (h *V1Handler) HandleChatCompletion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody)).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if stream, _ := req["stream"].(bool); stream {
		h.handleChatCompletionStream(w, r, req)
		return
	}
	resp, err := h.service.CreateTextCompletion(r.Context(), req)
	if err != nil {
		var backendErr service.TextBackendError
		if errors.As(err, &backendErr) {
			writeAPIError(w, backendErr.Status, backendErr.Body)
			return
		}
		if errors.Is(err, service.ErrTextBackendNotConfigured) {
			writeAPIError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(resp)
}

func (h *V1Handler) handleChatCompletionStream(w http.ResponseWriter, r *http.Request, req map[string]any) {
	resp, err := h.service.TextCompletionResponse(r.Context(), req)
	if err != nil {
		var backendErr service.TextBackendError
		if errors.As(err, &backendErr) {
			writeAPIError(w, backendErr.Status, backendErr.Body)
			return
		}
		if errors.Is(err, service.ErrTextBackendNotConfigured) {
			writeAPIError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	defer resp.Body.Close()

	dst := w.Header()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		dst.Set("Content-Type", ct)
	} else {
		dst.Set("Content-Type", "text/event-stream")
	}
	dst.Set("Cache-Control", "no-cache")
	dst.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	f, _ := w.(http.Flusher)
	if f != nil {
		f.Flush()
	}
	_, _ = io.Copy(flushWriter{w: w, f: f}, resp.Body)
}

type flushWriter struct {
	w io.Writer
	f http.Flusher
}

func (fw flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	if fw.f != nil {
		fw.f.Flush()
	}
	return n, err
}
