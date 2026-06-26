package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
