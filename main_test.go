package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDarkBeast6T2ICategory(t *testing.T) {
	s := &Server{}

	if got := s.workflowForCategory("t2i-darkbeast6"); got != darkBeast6T2IWorkflowID {
		t.Fatalf("workflowForCategory(t2i-darkbeast6) = %q, want %q", got, darkBeast6T2IWorkflowID)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/generate/categories", nil)
	rec := httptest.NewRecorder()

	s.handleGenerateCategories(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body struct {
		Categories []string          `json:"categories"`
		Endpoints  map[string]string `json:"endpoints"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}

	if body.Endpoints["t2i-darkbeast6"] != "/api/v1/generate/t2i-darkbeast6" {
		t.Fatalf("endpoint = %q, want %q", body.Endpoints["t2i-darkbeast6"], "/api/v1/generate/t2i-darkbeast6")
	}
	if !containsString(body.Categories, "t2i-darkbeast6") {
		t.Fatalf("categories = %#v, want t2i-darkbeast6", body.Categories)
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
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
