package test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"chat_image/app/service"
)

func TestComfyPromptQueued(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/queue" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{
			"queue_running": [[1, "running-id", {}, {}]],
			"queue_pending": [[2, "pending-id", {}, {}]]
		}`))
	}))
	defer backend.Close()

	client := service.NewComfyClient(backend.URL)
	for _, test := range []struct {
		promptID string
		want     bool
	}{
		{promptID: "running-id", want: true},
		{promptID: "pending-id", want: true},
		{promptID: "lost-id", want: false},
	} {
		got, err := client.PromptQueued(test.promptID)
		if err != nil {
			t.Fatalf("PromptQueued(%q): %v", test.promptID, err)
		}
		if got != test.want {
			t.Errorf("PromptQueued(%q) = %v, want %v", test.promptID, got, test.want)
		}
	}
}

func TestLostComfyPromptFailsTaskWithoutFullTimeout(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/prompt":
			_, _ = w.Write([]byte(`{"prompt_id":"lost-id"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/history/lost-id":
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && r.URL.Path == "/queue":
			_, _ = w.Write([]byte(`{"queue_running":[],"queue_pending":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer backend.Close()

	templatePath := filepath.Join(t.TempDir(), "workflow.json")
	if err := os.WriteFile(templatePath, []byte(`{"1":{"class_type":"Fake","inputs":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	server := service.NewServer(
		map[string]service.WorkflowConfig{"lost": {
			ID:             "lost",
			TemplatePath:   templatePath,
			TemplateFormat: "api",
			Mode:           "text_to_video",
			OutputKind:     "video",
			TimeoutSec:     60,
		}},
		service.NewComfyPool([]string{backend.URL}),
		service.ServiceConfig{},
		1,
		t.TempDir(),
		nil,
	)
	task, err := server.CreateTask(service.CreateTaskRequest{WorkflowID: "lost", Prompt: "test"})
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		current := server.GetTask(task.ID)
		if current.Status == "failed" {
			if !strings.Contains(current.Error, "disappeared from history and queue") {
				t.Fatalf("unexpected task error: %s", current.Error)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("task did not fail after prompt disappeared: %+v", server.GetTask(task.ID))
}
