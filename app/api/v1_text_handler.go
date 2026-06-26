package api

import (
	"encoding/json"
	"errors"
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
