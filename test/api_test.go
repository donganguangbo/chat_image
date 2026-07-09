package test

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"chat_image/app/api"
	"chat_image/app/model"
	"chat_image/app/service"
)

func newTestV1Server() *service.Server {
	workflows := map[string]service.WorkflowConfig{
		"wf_t2i":  {ID: "wf_t2i", Mode: "t2i", Defaults: map[string]any{}, Required: []string{"prompt"}},
		"wf_edit": {ID: "wf_edit", Mode: "image_edit", Defaults: map[string]any{}, Required: []string{"prompt", "input_image_base64"}},
		"wf_t2v":  {ID: "wf_t2v", Mode: "text_to_video", Defaults: map[string]any{}, Required: []string{"prompt"}},
		"wf_i2v":  {ID: "wf_i2v", Mode: "i2v", Defaults: map[string]any{}, Required: []string{"prompt", "input_image_url"}},
	}
	return service.NewServer(
		workflows,
		nil,
		service.ServiceConfig{DefaultWorkflows: service.CategoryWorkflowConfig{
			TextToImage:  "wf_t2i",
			ImageEdit:    "wf_edit",
			TextToVideo:  "wf_t2v",
			ImageToVideo: "wf_i2v",
		}},
		1,
		"",
		nil,
	)
}

func performV1Request(t *testing.T, s *service.Server, method, path, body, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	api.Register(mux, s, t.TempDir(), service.StorageModeLocal)

	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestV1ImageGenerationRouteCreatesTask(t *testing.T) {
	s := newTestV1Server()
	rec := performV1Request(t, s, http.MethodPost, "/v1/images/generations", `{"prompt":"a cat","size":"512x768","model":"local-image"}`, "application/json")

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

	task := s.GetTask(body.ID)
	if task == nil {
		t.Fatalf("task %q not found", body.ID)
	}
	if task.WorkflowID != "wf_t2i" || intParam(task.Params["width"]) != 512 || intParam(task.Params["height"]) != 768 {
		t.Fatalf("task = %+v", task)
	}
}

func TestV1ImageEditRouteAcceptsMultipartImage(t *testing.T) {
	s := newTestV1Server()
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

	rec := performV1Request(t, s, http.MethodPost, "/v1/images/edits", b.String(), w.FormDataContentType())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	task := s.GetTask(body.ID)
	if task == nil || task.WorkflowID != "wf_edit" || task.Params["input_image_base64"] == "" {
		t.Fatalf("task = %+v", task)
	}
}

func TestV1ImageEditRouteKeepsRequestedSize(t *testing.T) {
	s := newTestV1Server()
	rec := performV1Request(t, s, http.MethodPost, "/v1/images/edits", `{
		"prompt": "edit it",
		"size": "1024x1280",
		"image_base64": "png bytes",
		"params": {"width": 576, "height": 720}
	}`, "application/json")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	task := s.GetTask(body.ID)
	if task == nil || task.WorkflowID != "wf_edit" || intParam(task.Params["width"]) != 1024 || intParam(task.Params["height"]) != 1280 {
		t.Fatalf("task = %+v", task)
	}
}

func TestV1VideoRoutes(t *testing.T) {
	s := newTestV1Server()
	rec := performV1Request(t, s, http.MethodPost, "/v1/videos", `{"prompt":"move","size":"1280x720","seconds":4}`, "application/json")
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

	rec = performV1Request(t, s, http.MethodGet, "/v1/videos/"+created.ID, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	s.UpdateTask(created.ID, func(tk *model.Task) {
		tk.Status = "completed"
		tk.Outputs = []model.TaskOutput{{Kind: "video", URL: "/outputs/videos/out.mp4"}}
	})
	rec = performV1Request(t, s, http.MethodGet, "/v1/videos/"+created.ID+"/content", "", "")
	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("content status = %d, want %d; body=%s", rec.Code, http.StatusTemporaryRedirect, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/outputs/videos/out.mp4" {
		t.Fatalf("Location = %q, want /outputs/videos/out.mp4", got)
	}
}

func TestLegacyGenerateRoutesAreNotRegistered(t *testing.T) {
	s := newTestV1Server()
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
		rec := performV1Request(t, s, http.MethodPost, path, `{}`, "application/json")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want %d", path, rec.Code, http.StatusNotFound)
		}
	}
}

func intParam(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	default:
		return 0
	}
}
