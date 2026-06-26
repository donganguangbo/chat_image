package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"chat_image/app/service"
)

func (h *V1Handler) HandleImageGeneration(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req V1ImageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	h.createImageTask(w, "t2i", "image.generation", req)
}

func (h *V1Handler) HandleImageEdit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	req, err := parseImageEditRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.createImageTask(w, "edit", "image.edit", req)
}

func (h *V1Handler) createImageTask(w http.ResponseWriter, category, object string, req V1ImageRequest) {
	workflowID := h.service.WorkflowForCategory(category)
	if workflowID == "" {
		writeAPIError(w, http.StatusBadRequest, "category workflow not configured")
		return
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		writeAPIError(w, http.StatusBadRequest, "prompt is required")
		return
	}
	width, height, err := parseSize(req.Size)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	params := cloneMap(req.Params)
	copyCommonParams(params, req.Model, req.User)
	if req.Quality != "" {
		params["quality"] = req.Quality
	}
	if req.Style != "" {
		params["style"] = req.Style
	}
	if req.N > 0 {
		params["n"] = req.N
	}
	if req.ResponseFormat != "" {
		params["response_format"] = req.ResponseFormat
	}
	if req.InputImage != "" {
		params["input_image"] = strings.TrimSpace(req.InputImage)
	}
	if req.InputImageURL != "" {
		params["input_image_url"] = strings.TrimSpace(req.InputImageURL)
	}
	if req.InputImageBase64 != "" {
		params["input_image_base64"] = strings.TrimSpace(req.InputImageBase64)
	}

	task, err := h.service.CreateTask(service.CreateTaskRequest{
		WorkflowID: workflowID,
		Prompt:     prompt,
		Width:      width,
		Height:     height,
		Params:     params,
	})
	if err != nil {
		writeTaskError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v1ResponseFromTask(task, object))
}

func (h *V1Handler) HandleVideoCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req V1VideoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		writeAPIError(w, http.StatusBadRequest, "prompt is required")
		return
	}
	width, height, err := parseSize(req.Size)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	params := cloneMap(req.Params)
	copyCommonParams(params, req.Model, req.User)
	duration := req.DurationSec
	if duration == 0 {
		duration = req.Seconds
	}
	if duration > 0 {
		params["duration_sec"] = duration
	}
	category := "t2v"
	if v := strings.TrimSpace(req.InputReference.Image); v != "" {
		params["input_image"] = v
		category = "i2v"
	}
	if v := strings.TrimSpace(req.InputReference.ImageURL); v != "" {
		params["input_image_url"] = v
		category = "i2v"
	}
	if v := strings.TrimSpace(req.InputReference.ImageBase64); v != "" {
		params["input_image_base64"] = v
		category = "i2v"
	}

	workflowID := h.service.WorkflowForCategory(category)
	if workflowID == "" {
		writeAPIError(w, http.StatusBadRequest, "category workflow not configured")
		return
	}
	task, err := h.service.CreateTask(service.CreateTaskRequest{
		WorkflowID: workflowID,
		Prompt:     prompt,
		Width:      width,
		Height:     height,
		Params:     params,
	})
	if err != nil {
		writeTaskError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v1ResponseFromTask(task, "video"))
}

func (h *V1Handler) HandleVideoByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/videos/")
	if path == "" {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(path, "/")
	id := parts[0]
	if len(parts) == 2 && parts[1] == "content" {
		h.handleVideoContent(w, r, id)
		return
	}
	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		task := h.service.GetTask(id)
		if task == nil {
			writeAPIError(w, http.StatusNotFound, "video not found")
			return
		}
		writeJSON(w, http.StatusOK, v1ResponseFromTask(task, "video"))
	case http.MethodDelete:
		status, err := h.service.CancelTask(id)
		if err != nil {
			writeAPIError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "object": "video.deleted", "deleted": status == "cancelled"})
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *V1Handler) handleVideoContent(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	task := h.service.GetTask(id)
	if task == nil {
		writeAPIError(w, http.StatusNotFound, "video not found")
		return
	}
	if task.Status != "completed" {
		writeAPIError(w, http.StatusConflict, "video is not completed")
		return
	}
	for _, out := range task.Outputs {
		if out.Kind == "video" && out.URL != "" {
			http.Redirect(w, r, out.URL, http.StatusTemporaryRedirect)
			return
		}
	}
	writeAPIError(w, http.StatusNotFound, "video content not found")
}
