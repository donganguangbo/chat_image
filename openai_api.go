package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type OpenAIImageRequest struct {
	Model          string         `json:"model"`
	Prompt         string         `json:"prompt"`
	Size           string         `json:"size"`
	Quality        string         `json:"quality"`
	Style          string         `json:"style"`
	N              int            `json:"n"`
	ResponseFormat string         `json:"response_format"`
	User           string         `json:"user"`
	Params         map[string]any `json:"params"`

	InputImage       string `json:"image"`
	InputImageURL    string `json:"image_url"`
	InputImageBase64 string `json:"image_base64"`
}

type OpenAIVideoRequest struct {
	Model          string         `json:"model"`
	Prompt         string         `json:"prompt"`
	Size           string         `json:"size"`
	Seconds        int            `json:"seconds"`
	DurationSec    int            `json:"duration_sec"`
	User           string         `json:"user"`
	Params         map[string]any `json:"params"`
	InputReference struct {
		ImageURL    string `json:"image_url"`
		ImageBase64 string `json:"image_base64"`
		Image       string `json:"image"`
	} `json:"input_reference"`
}

type OpenAIOutputData struct {
	URL     string `json:"url,omitempty"`
	B64JSON string `json:"b64_json,omitempty"`
}

type OpenAITaskResponse struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Status  string             `json:"status"`
	Model   string             `json:"model,omitempty"`
	Error   string             `json:"error,omitempty"`
	Data    []OpenAIOutputData `json:"data,omitempty"`
}

type OpenAIErrorResponse struct {
	Error OpenAIError `json:"error"`
}

type OpenAIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

func (s *Server) workflowForCategory(category string) string {
	switch category {
	case "t2i":
		return strings.TrimSpace(s.service.DefaultWorkflows.TextToImage)
	case "edit":
		return strings.TrimSpace(s.service.DefaultWorkflows.ImageEdit)
	case "t2v":
		return strings.TrimSpace(s.service.DefaultWorkflows.TextToVideo)
	case "i2v":
		return strings.TrimSpace(s.service.DefaultWorkflows.ImageToVideo)
	default:
		return ""
	}
}

func (s *Server) handleOpenAIImageGeneration(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req OpenAIImageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	s.createOpenAIImageTask(w, "t2i", "image.generation", req)
}

func (s *Server) handleOpenAIImageEdit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	req, err := parseOpenAIImageEditRequest(r)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.createOpenAIImageTask(w, "edit", "image.edit", req)
}

func (s *Server) createOpenAIImageTask(w http.ResponseWriter, category, object string, req OpenAIImageRequest) {
	workflowID := s.workflowForCategory(category)
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

	task, err := s.createTask(CreateTaskRequest{
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

func (s *Server) handleOpenAIVideoCreate(w http.ResponseWriter, r *http.Request) {
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

	workflowID := s.workflowForCategory(category)
	if workflowID == "" {
		writeOpenAIError(w, http.StatusBadRequest, "category workflow not configured")
		return
	}
	task, err := s.createTask(CreateTaskRequest{
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

func (s *Server) handleOpenAIVideoByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/videos/")
	if path == "" {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(path, "/")
	id := parts[0]
	if len(parts) == 2 && parts[1] == "content" {
		s.handleOpenAIVideoContent(w, r, id)
		return
	}
	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		task := s.getTask(id)
		if task == nil {
			writeOpenAIError(w, http.StatusNotFound, "video not found")
			return
		}
		writeJSON(w, http.StatusOK, openAIResponseFromTask(task, "video"))
	case http.MethodDelete:
		status, err := s.cancelTask(id)
		if err != nil {
			writeOpenAIError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "object": "video.deleted", "deleted": status == "cancelled"})
	default:
		writeOpenAIError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleOpenAIVideoContent(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	task := s.getTask(id)
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

func parseOpenAIImageEditRequest(r *http.Request) (OpenAIImageRequest, error) {
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		if err := r.ParseMultipartForm(maxRequestBody); err != nil {
			return OpenAIImageRequest{}, fmt.Errorf("invalid multipart body")
		}
		if r.MultipartForm != nil {
			defer func() { _ = r.MultipartForm.RemoveAll() }()
		}
		req := OpenAIImageRequest{
			Model:          r.FormValue("model"),
			Prompt:         r.FormValue("prompt"),
			Size:           r.FormValue("size"),
			Quality:        r.FormValue("quality"),
			ResponseFormat: r.FormValue("response_format"),
			User:           r.FormValue("user"),
			Params:         map[string]any{},
		}
		if imageURL := strings.TrimSpace(r.FormValue("image_url")); imageURL != "" {
			req.InputImageURL = imageURL
		}
		if r.MultipartForm != nil {
			if files := r.MultipartForm.File["image"]; len(files) > 0 {
				f, err := files[0].Open()
				if err != nil {
					return OpenAIImageRequest{}, fmt.Errorf("read image file: %w", err)
				}
				defer f.Close()
				b, err := io.ReadAll(f)
				if err != nil {
					return OpenAIImageRequest{}, fmt.Errorf("read image file: %w", err)
				}
				req.InputImageBase64 = base64.StdEncoding.EncodeToString(b)
			}
			if files := r.MultipartForm.File["mask"]; len(files) > 0 {
				f, err := files[0].Open()
				if err != nil {
					return OpenAIImageRequest{}, fmt.Errorf("read mask file: %w", err)
				}
				defer f.Close()
				b, err := io.ReadAll(f)
				if err != nil {
					return OpenAIImageRequest{}, fmt.Errorf("read mask file: %w", err)
				}
				req.Params["mask_base64"] = base64.StdEncoding.EncodeToString(b)
			}
		}
		return req, nil
	}

	var req OpenAIImageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return OpenAIImageRequest{}, fmt.Errorf("invalid JSON body")
	}
	return req, nil
}

func parseOpenAISize(size string) (int, int, error) {
	size = strings.TrimSpace(size)
	if size == "" || size == "auto" {
		return 0, 0, nil
	}
	parts := strings.Split(size, "x")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid size %q", size)
	}
	w, err := strconv.Atoi(parts[0])
	if err != nil || w <= 0 {
		return 0, 0, fmt.Errorf("invalid size %q", size)
	}
	h, err := strconv.Atoi(parts[1])
	if err != nil || h <= 0 {
		return 0, 0, fmt.Errorf("invalid size %q", size)
	}
	return w, h, nil
}

func copyOpenAICommonParams(params map[string]any, model, user string) {
	if model = strings.TrimSpace(model); model != "" {
		params["model"] = model
	}
	if user = strings.TrimSpace(user); user != "" {
		params["user"] = user
	}
}

func openAIResponseFromTask(task *Task, object string) OpenAITaskResponse {
	resp := OpenAITaskResponse{
		ID:      task.ID,
		Object:  object,
		Created: task.CreatedAt.Unix(),
		Status:  openAIStatus(task.Status),
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
		resp.Data = append(resp.Data, OpenAIOutputData{URL: out.URL})
	}
	return resp
}

func openAIStatus(status string) string {
	if status == "running" {
		return "in_progress"
	}
	return status
}

func writeOpenAITaskError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrQueueFull) {
		writeOpenAIError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeOpenAIError(w, http.StatusBadRequest, err.Error())
}

func writeOpenAIError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, OpenAIErrorResponse{Error: OpenAIError{
		Message: message,
		Type:    "invalid_request_error",
	}})
}
