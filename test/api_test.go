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
		"wf_t2v":  {ID: "wf_t2v", Mode: "text_to_video", Defaults: map[string]any{}, Required: []string{"motion_prompt", "motion_negative_prompt"}},
		"wf_i2v":  {ID: "wf_i2v", Mode: "i2v", Defaults: map[string]any{}, Required: []string{"motion_prompt", "motion_negative_prompt", "first_frame_prompt", "first_frame_negative_prompt", "source_image"}},
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
	rec := performV1Request(t, s, http.MethodPost, "/v1/videos", `{"motion_prompt":"move continuously","motion_negative_prompt":"camera movement","size":"1280x720","seconds":4}`, "application/json")
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
	if task := s.GetTask(created.ID); task == nil {
		t.Fatalf("task %q not found", created.ID)
	} else if _, exists := task.Params["action_id"]; exists {
		t.Fatalf("action_id must be absent for legacy clients: %+v", task.Params)
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

func TestV1VideoActionIDIsOptional(t *testing.T) {
	s := newTestV1Server()
	for _, test := range []struct {
		name       string
		actionJSON string
		wantAction string
	}{
		{name: "provided", actionJSON: `"action_id":"cumshot",`, wantAction: "cumshot"},
		{name: "empty", actionJSON: `"action_id":"   ",`},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := `{` + test.actionJSON + `"motion_prompt":"move continuously","motion_negative_prompt":"camera movement","size":"1280x720"}`
			rec := performV1Request(t, s, http.MethodPost, "/v1/videos", body, "application/json")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
			}
			var created struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
				t.Fatal(err)
			}
			task := s.GetTask(created.ID)
			if task == nil {
				t.Fatalf("task %q not found", created.ID)
			}
			got, exists := task.Params["action_id"]
			if test.wantAction == "" {
				if exists {
					t.Fatalf("empty action_id must be omitted, got %v", got)
				}
			} else if got != test.wantAction {
				t.Fatalf("action_id = %v, want %q", got, test.wantAction)
			}
		})
	}

	rec := performV1Request(t, s, http.MethodPost, "/v1/videos", `{
		"action_id":"Cum Shot",
		"motion_prompt":"move continuously",
		"motion_negative_prompt":"camera movement",
		"size":"1280x720"
	}`, "application/json")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "action_id") {
		t.Fatalf("invalid action status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestV1ImageToVideoUsesExplicitStageInputs(t *testing.T) {
	s := newTestV1Server()
	rec := performV1Request(t, s, http.MethodPost, "/v1/videos", `{
		"motion_prompt":"continuous movement",
		"motion_negative_prompt":"camera movement, shot change",
		"first_frame_prompt":"configured first frame",
		"first_frame_negative_prompt":"bad anatomy, fused bodies",
		"source_image":{"image_url":"https://cdn.example/source.png"},
		"partner_identity_image":{"image_base64":"data:image/png;base64,partner"},
		"size":"1024x1280",
		"seconds":5,
		"resolution":"480p",
		"quality":"standard"
	}`, "application/json")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	task := s.GetTask(created.ID)
	if task == nil || task.WorkflowID != "wf_i2v" {
		t.Fatalf("task = %+v", task)
	}
	for key, want := range map[string]string{
		"motion_prompt":                 "continuous movement",
		"motion_negative_prompt":        "camera movement, shot change",
		"first_frame_prompt":            "configured first frame",
		"first_frame_negative_prompt":   "bad anatomy, fused bodies",
		"source_image_url":              "https://cdn.example/source.png",
		"partner_identity_image_base64": "data:image/png;base64,partner",
	} {
		if got := task.Params[key]; got != want {
			t.Fatalf("params[%s] = %v, want %q", key, got, want)
		}
	}
}

func TestV1VideoAcceptsLegacyTextContract(t *testing.T) {
	s := newTestV1Server()
	rec := performV1Request(t, s, http.MethodPost, "/v1/videos", `{
		"prompt":"move continuously",
		"size":"1280x720",
		"duration_sec":4,
		"params":{"resolution":"480p","quality":"standard"}
	}`, "application/json")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	task := s.GetTask(created.ID)
	if task == nil || task.WorkflowID != "wf_t2v" {
		t.Fatalf("task = %+v", task)
	}
	for key, want := range map[string]any{
		"motion_prompt":          "move continuously",
		"motion_negative_prompt": "camera movement, shot change, slow motion, frozen motion, extra person, temporal discontinuity",
		"resolution":             "480p",
		"quality":                "standard",
	} {
		if got := task.Params[key]; got != want {
			t.Fatalf("params[%s] = %v, want %v", key, got, want)
		}
	}
	if got := intParam(task.Params["duration_sec"]); got != 4 {
		t.Fatalf("params[duration_sec] = %v, want 4", task.Params["duration_sec"])
	}
}

func TestV1VideoAcceptsLegacyImageContract(t *testing.T) {
	s := newTestV1Server()
	rec := performV1Request(t, s, http.MethodPost, "/v1/videos", `{
		"prompt":"continuous movement",
		"input_reference":{"image_url":"https://cdn.example/source.png"},
		"size":"1024x1280",
		"seconds":5,
		"params":{
			"negative_prompt":"bad anatomy, camera movement",
			"image_edit_prompt":"configured first frame",
			"input_image_end_base64":"data:image/png;base64,partner",
			"resolution":"480p",
			"quality":"standard"
		}
	}`, "application/json")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	task := s.GetTask(created.ID)
	if task == nil || task.WorkflowID != "wf_i2v" {
		t.Fatalf("task = %+v", task)
	}
	for key, want := range map[string]string{
		"motion_prompt":                 "continuous movement",
		"motion_negative_prompt":        "bad anatomy, camera movement",
		"first_frame_prompt":            "configured first frame",
		"first_frame_negative_prompt":   "bad anatomy, camera movement",
		"source_image_url":              "https://cdn.example/source.png",
		"partner_identity_image_base64": "data:image/png;base64,partner",
	} {
		if got := task.Params[key]; got != want {
			t.Fatalf("params[%s] = %v, want %q", key, got, want)
		}
	}
}

func TestV1VideoRejectsMixedContracts(t *testing.T) {
	s := newTestV1Server()
	rec := performV1Request(t, s, http.MethodPost, "/v1/videos", `{
		"prompt":"legacy motion",
		"motion_prompt":"explicit motion",
		"motion_negative_prompt":"camera movement"
	}`, "application/json")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "must not mix") {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
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
