package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestOpenAIServer() *Server {
	workflows := map[string]WorkflowConfig{
		"wf_t2i":  {ID: "wf_t2i", Mode: "t2i", Defaults: map[string]any{}, Required: []string{"prompt"}},
		"wf_edit": {ID: "wf_edit", Mode: "image_edit", Defaults: map[string]any{}, Required: []string{"prompt", "input_image_base64"}},
		"wf_t2v":  {ID: "wf_t2v", Mode: "text_to_video", Defaults: map[string]any{}, Required: []string{"prompt"}},
		"wf_i2v":  {ID: "wf_i2v", Mode: "i2v", Defaults: map[string]any{}, Required: []string{"prompt", "input_image_url"}},
	}
	return &Server{
		workflows: workflows,
		tasks:     map[string]*Task{},
		queue:     make(chan string, 8),
		service: ServiceConfig{DefaultWorkflows: CategoryWorkflowConfig{
			TextToImage:  "wf_t2i",
			ImageEdit:    "wf_edit",
			TextToVideo:  "wf_t2v",
			ImageToVideo: "wf_i2v",
		}},
	}
}

func performOpenAIRequest(t *testing.T, s *Server, method, path, body, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	registerRoutes(mux, s, t.TempDir())

	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestOpenAIImageGenerationRouteCreatesTask(t *testing.T) {
	s := newTestOpenAIServer()
	rec := performOpenAIRequest(t, s, http.MethodPost, "/v1/images/generations", `{"prompt":"a cat","size":"512x768","model":"local-image"}`, "application/json")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body struct {
		ID     string `json:"id"`
		Object string `json:"object"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}
	if body.ID == "" || body.Object != "image.generation" || body.Status != "queued" {
		t.Fatalf("response = %+v", body)
	}

	task := s.getTask(body.ID)
	if task == nil {
		t.Fatalf("task %q not found", body.ID)
	}
	if task.WorkflowID != "wf_t2i" || getInt(task.Params["width"], 0) != 512 || getInt(task.Params["height"], 0) != 768 {
		t.Fatalf("task = %+v", task)
	}
}

func TestOpenAIImageEditRouteAcceptsMultipartImage(t *testing.T) {
	s := newTestOpenAIServer()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	_ = w.WriteField("prompt", "edit it")
	_ = w.WriteField("size", "640x480")
	part, err := w.CreateFormFile("image", "input.png")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	_, _ = part.Write([]byte("png bytes"))
	if err := w.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}

	rec := performOpenAIRequest(t, s, http.MethodPost, "/v1/images/edits", b.String(), w.FormDataContentType())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	task := s.getTask(body.ID)
	if task == nil || task.WorkflowID != "wf_edit" || task.Params["input_image_base64"] == "" {
		t.Fatalf("task = %+v", task)
	}
}

func TestOpenAIVideoRoutes(t *testing.T) {
	s := newTestOpenAIServer()
	rec := performOpenAIRequest(t, s, http.MethodPost, "/v1/videos", `{"prompt":"move","size":"1280x720","seconds":4}`, "application/json")
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var created struct {
		ID     string `json:"id"`
		Object string `json:"object"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.ID == "" || created.Object != "video" || created.Status != "queued" {
		t.Fatalf("created = %+v", created)
	}

	rec = performOpenAIRequest(t, s, http.MethodGet, "/v1/videos/"+created.ID, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	s.updateTask(created.ID, func(tk *Task) {
		tk.Status = "completed"
		tk.Outputs = []TaskOutput{{Kind: "video", URL: "/outputs/videos/out.mp4"}}
	})
	rec = performOpenAIRequest(t, s, http.MethodGet, "/v1/videos/"+created.ID+"/content", "", "")
	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("content status = %d, want %d; body=%s", rec.Code, http.StatusTemporaryRedirect, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/outputs/videos/out.mp4" {
		t.Fatalf("Location = %q, want /outputs/videos/out.mp4", got)
	}
}

func TestLegacyGenerateRoutesAreNotRegistered(t *testing.T) {
	s := newTestOpenAIServer()
	for _, path := range []string{
		"/api/v1/generate/categories",
		"/api/v1/generate/t2i",
		"/api/v1/generate/edit",
		"/api/v1/generate/t2v",
		"/api/v1/generate/i2v",
		"/api/v1/tasks",
		"/api/v1/tasks/task-id",
		"/api/v1/tasks/task-id/stream",
		"/api/v1/tasks/task-id/cancel",
		"/api/v1/workflows",
	} {
		rec := performOpenAIRequest(t, s, http.MethodPost, path, `{}`, "application/json")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want %d", path, rec.Code, http.StatusNotFound)
		}
	}
}

func TestLoadWorkflowsAllowsMissingOrEmptyDir(t *testing.T) {
	got, err := loadWorkflows(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatalf("missing dir err = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("missing dir workflows = %d, want 0", len(got))
	}

	got, err = loadWorkflows(t.TempDir())
	if err != nil {
		t.Fatalf("empty dir err = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty dir workflows = %d, want 0", len(got))
	}
}

func TestLoadServiceConfigAllowsNoWorkflows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.yaml")
	if err := os.WriteFile(path, []byte("default_workflows:\n  text_to_image: deleted\n"), 0o600); err != nil {
		t.Fatalf("write service config: %v", err)
	}

	if _, err := loadServiceConfig(path, map[string]WorkflowConfig{}); err != nil {
		t.Fatalf("loadServiceConfig with no workflows err = %v", err)
	}
}
