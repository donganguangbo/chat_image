package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"chat_image/app/service"
)

func modelsTestServer(t *testing.T, textModel string, textBaseURL string, defaultWorkflows service.CategoryWorkflowConfig) *service.Server {
	t.Helper()
	workflows := map[string]service.WorkflowConfig{
		"t2i":  {ID: "t2i"},
		"edit": {ID: "edit"},
		"t2v":  {ID: "t2v"},
		"i2v":  {ID: "i2v"},
	}
	cfg := service.ServiceConfig{
		StorageMode:      service.StorageModeLocal,
		DefaultWorkflows: defaultWorkflows,
		Text:             service.TextConfig{Model: textModel, BaseURL: textBaseURL},
	}
	return service.NewServer(workflows, nil, cfg, 0, t.TempDir(), nil)
}

// TestListModelsAdvertisesConfiguredModels 文本后端 + 已配置图像工作流 → 列表里
// 该有的模型都有,未配置的分类不出现。
func TestListModelsAdvertisesConfiguredModels(t *testing.T) {
	s := modelsTestServer(t, "qwen-local", "http://127.0.0.1:8000", service.CategoryWorkflowConfig{
		TextToImage: "t2i",
		TextToVideo: "t2v",
	})
	h := &V1Handler{service: s}
	rec := httptest.NewRecorder()

	h.HandleListModels(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp V1ModelList
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if resp.Object != "list" {
		t.Errorf("object=%q, want list", resp.Object)
	}
	ids := map[string]bool{}
	for _, m := range resp.Data {
		if m.ID == "" || m.Object != "model" || m.Created == 0 {
			t.Errorf("模型字段不完整: %+v", m)
		}
		ids[m.ID] = true
	}
	if !ids["qwen-local"] {
		t.Errorf("缺文本模型 qwen-local: %v", ids)
	}
	if !ids["gpt-image-1"] {
		t.Errorf("缺 t2i 别名 gpt-image-1: %v", ids)
	}
	if !ids["sora-1"] {
		t.Errorf("缺 t2v 别名 sora-1: %v", ids)
	}
	// edit / i2v 未配置默认工作流 → 不应出现
	if ids["gpt-image-edit-1"] || ids["image-to-video"] {
		t.Errorf("未配置的分类不应出现: %v", ids)
	}
}

// TestListModelsTextBackendDisabled 文本后端未启用 → 不暴露文本模型,但已配置
// 的图像分类仍在。
func TestListModelsTextBackendDisabled(t *testing.T) {
	s := modelsTestServer(t, "", "", service.CategoryWorkflowConfig{TextToImage: "t2i"})
	h := &V1Handler{service: s}
	rec := httptest.NewRecorder()

	h.HandleListModels(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))

	var resp V1ModelList
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	ids := map[string]bool{}
	for _, m := range resp.Data {
		ids[m.ID] = true
	}
	if ids["qwen-local"] {
		t.Errorf("文本后端禁用时不该有 qwen-local: %v", ids)
	}
	if !ids["gpt-image-1"] {
		t.Errorf("图像别名应保留: %v", ids)
	}
}

// TestListModelsMethodNotAllowed 非 GET → 405。
func TestListModelsMethodNotAllowed(t *testing.T) {
	s := modelsTestServer(t, "qwen", "http://127.0.0.1:8000", service.CategoryWorkflowConfig{})
	h := &V1Handler{service: s}
	rec := httptest.NewRecorder()
	h.HandleListModels(rec, httptest.NewRequest(http.MethodPost, "/v1/models", strings.NewReader("{}")))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code=%d, want 405", rec.Code)
	}
}
