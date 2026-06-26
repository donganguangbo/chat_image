package test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"chat_image/app/service"
)

type failingStorage struct{}

func (failingStorage) UploadPrivate(context.Context, string, io.Reader, string) error {
	return errors.New("upload failed")
}

func (failingStorage) PresignGetObject(context.Context, string, time.Duration) (string, error) {
	return "", nil
}

func TestStorageModeMustBeExplicit(t *testing.T) {
	for _, mode := range []string{"", "disk"} {
		if _, err := service.NormalizeStorageMode(mode); err == nil {
			t.Fatalf("NormalizeStorageMode(%q) error = nil, want error", mode)
		}
	}
	for _, mode := range []string{"local", "s3"} {
		got, err := service.NormalizeStorageMode(mode)
		if err != nil {
			t.Fatalf("NormalizeStorageMode(%q): %v", mode, err)
		}
		if got != mode {
			t.Fatalf("NormalizeStorageMode(%q) = %q, want %q", mode, got, mode)
		}
	}
}

func TestS3ModeFailsTaskWhenUploadFails(t *testing.T) {
	comfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/prompt":
			_ = json.NewEncoder(w).Encode(map[string]string{"prompt_id": "p1"})
		case r.Method == http.MethodGet && r.URL.Path == "/history/p1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"p1": map[string]any{
					"status": map[string]any{"status_str": "success"},
					"outputs": map[string]any{
						"9": map[string]any{
							"images": []map[string]any{{
								"filename":  "out.avif",
								"subfolder": "",
								"type":      "output",
							}},
						},
					},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/view":
			_, _ = w.Write([]byte("avif bytes"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer comfy.Close()

	templatePath := filepath.Join(t.TempDir(), "workflow.json")
	if err := os.WriteFile(templatePath, []byte(`{"1":{"class_type":"SaveImage","inputs":{}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	s := service.NewServer(
		map[string]service.WorkflowConfig{"wf": {
			ID:             "wf",
			Mode:           "t2i",
			TemplatePath:   templatePath,
			TemplateFormat: "api",
			Defaults:       map[string]any{},
			Required:       []string{"prompt"},
			TimeoutSec:     2,
		}},
		service.NewComfyPool([]string{comfy.URL}),
		service.ServiceConfig{StorageMode: "s3"},
		1,
		t.TempDir(),
		failingStorage{},
	)

	task, err := s.CreateTask(service.CreateTaskRequest{WorkflowID: "wf", Prompt: "cat"})
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		got := s.GetTask(task.ID)
		if got != nil && got.Status == "failed" {
			if !strings.Contains(got.Error, "upload failed") {
				t.Fatalf("task error = %q, want upload failure", got.Error)
			}
			return
		}
		if got != nil && got.Status == "completed" {
			t.Fatalf("task completed despite S3 upload failure: %+v", got.Outputs)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("task did not fail before timeout: %+v", s.GetTask(task.ID))
}
