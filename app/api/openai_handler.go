package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"chat_image/app/service"
)

func (h *OpenAIHandler) HandleImageGeneration(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req OpenAIImageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	h.createOpenAIImageTask(w, "t2i", "image.generation", req)
}

func (h *OpenAIHandler) HandleImageEdit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	req, err := parseOpenAIImageEditRequest(r)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.createOpenAIImageTask(w, "edit", "image.edit", req)
}

func (h *OpenAIHandler) createOpenAIImageTask(w http.ResponseWriter, category, object string, req OpenAIImageRequest) {
	workflowID := h.service.WorkflowForCategory(category)
	if workflowID == "" {
		writeOpenAIError(w, http.StatusBadRequest, "category workflow not configured")
		return
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		writeOpenAIError(w, http.StatusBadRequest, "prompt is required")
		return
	}
	width, height, err := parseOpenAISize(req.Size)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	params := cloneMap(req.Params)
	copyOpenAICommonParams(params, req.Model, req.User)
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
		writeOpenAITaskError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, openAIResponseFromTask(task, object))
}

func (h *OpenAIHandler) HandleVideoCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req OpenAIVideoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		writeOpenAIError(w, http.StatusBadRequest, "prompt is required")
		return
	}
	width, height, err := parseOpenAISize(req.Size)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}

	params := cloneMap(req.Params)
	copyOpenAICommonParams(params, req.Model, req.User)
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
		writeOpenAIError(w, http.StatusBadRequest, "category workflow not configured")
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
		writeOpenAITaskError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, openAIResponseFromTask(task, "video"))
}

func (h *OpenAIHandler) HandleVideoByID(w http.ResponseWriter, r *http.Request) {
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
			writeOpenAIError(w, http.StatusNotFound, "video not found")
			return
		}
		writeJSON(w, http.StatusOK, openAIResponseFromTask(task, "video"))
	case http.MethodDelete:
		status, err := h.service.CancelTask(id)
		if err != nil {
			writeOpenAIError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "object": "video.deleted", "deleted": status == "cancelled"})
	default:
		writeOpenAIError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *OpenAIHandler) handleVideoContent(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	task := h.service.GetTask(id)
	if task == nil {
		writeOpenAIError(w, http.StatusNotFound, "video not found")
		return
	}
	if task.Status != "completed" {
		writeOpenAIError(w, http.StatusConflict, "video is not completed")
		return
	}
	for _, out := range task.Outputs {
		if out.Kind == "video" && out.URL != "" {
			http.Redirect(w, r, out.URL, http.StatusTemporaryRedirect)
			return
		}
	}
	writeOpenAIError(w, http.StatusNotFound, "video content not found")
}
