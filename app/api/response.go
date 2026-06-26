package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"chat_image/app/service"
)

func v1ResponseFromTask(task *service.Task, object string) V1TaskResponse {
	resp := V1TaskResponse{
		ID:      task.ID,
		Object:  object,
		Created: task.CreatedAt.Unix(),
		Status:  v1Status(task.Status),
		Error:   task.Error,
	}
	if model := asString(task.Params["model"]); model != "" {
		resp.Model = model
	}
	for _, out := range task.Outputs {
		if out.URL == "" {
			continue
		}
		if strings.HasPrefix(object, "image.") && out.Kind != "image" {
			continue
		}
		resp.Data = append(resp.Data, V1OutputData{URL: out.URL})
	}
	return resp
}

func v1Status(status string) string {
	if status == "running" {
		return "in_progress"
	}
	return status
}

func writeTaskError(w http.ResponseWriter, err error) {
	if errors.Is(err, service.ErrQueueFull) {
		writeAPIError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeAPIError(w, http.StatusBadRequest, err.Error())
}

func writeAPIError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, APIErrorResponse{Error: APIError{
		Message: message,
		Type:    "invalid_request_error",
	}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func cloneMap(in map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}
