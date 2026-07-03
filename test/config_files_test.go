package test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"chat_image/app/model"
	"gopkg.in/yaml.v3"
)

func TestDefaultWorkflowConfigFilesExist(t *testing.T) {
	var serviceCfg struct {
		DefaultWorkflows map[string]string `yaml:"default_workflows"`
	}
	raw, err := os.ReadFile(filepath.Join("..", "app", "configs", "service.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(raw, &serviceCfg); err != nil {
		t.Fatal(err)
	}

	workflowsDir := filepath.Join("..", "app", "configs", "workflows")
	entries, err := os.ReadDir(workflowsDir)
	if err != nil {
		t.Fatal(err)
	}
	workflowFiles := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(workflowsDir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			ID       string `yaml:"id"`
			Template string `yaml:"template"`
		}
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			t.Fatal(err)
		}
		workflowFiles[cfg.ID] = path
		if cfg.Template == "" {
			t.Fatalf("%s has empty template", path)
		}
		if _, err := os.Stat(filepath.Clean(filepath.Join(filepath.Dir(path), cfg.Template))); err != nil {
			t.Fatalf("%s template %q: %v", path, cfg.Template, err)
		}
	}

	for category, workflowID := range serviceCfg.DefaultWorkflows {
		if workflowID == "" {
			continue
		}
		if _, ok := workflowFiles[workflowID]; !ok {
			t.Fatalf("default_workflows.%s points to missing workflow %q", category, workflowID)
		}
	}
}

func TestServiceConfigParsesGatewaySecret(t *testing.T) {
	var cfg model.ServiceConfig
	if err := yaml.Unmarshal([]byte("gateway_secret: test-secret\nstorage_mode: s3\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.GatewaySecret != "test-secret" {
		t.Fatalf("gateway_secret = %q", cfg.GatewaySecret)
	}
}

func TestImageEditWorkflowKeepsReferenceImages(t *testing.T) {
	cfgRaw, err := os.ReadFile(filepath.Join("..", "app", "configs", "workflows", "ichat_image_edit.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg model.WorkflowConfig
	if err := yaml.Unmarshal(cfgRaw, &cfg); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cfg.Required, "input_image") {
		t.Fatalf("image edit required = %v, want input_image", cfg.Required)
	}

	workflowRaw, err := os.ReadFile(filepath.Join("..", "workflows", "ichat_image_edit.json"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow map[string]map[string]any
	if err := json.Unmarshal(workflowRaw, &workflow); err != nil {
		t.Fatal(err)
	}
	hasLoadImage := false
	hasGetImageSize := false
	for _, node := range workflow {
		switch node["class_type"] {
		case "LoadImage":
			hasLoadImage = true
		case "GetImageSize":
			hasGetImageSize = true
		}
	}
	if !hasLoadImage || !hasGetImageSize {
		t.Fatalf("image edit workflow must keep reference image nodes (LoadImage=%v GetImageSize=%v)", hasLoadImage, hasGetImageSize)
	}
}

func TestTextToImageWorkflowHasNoReferenceImages(t *testing.T) {
	cfgRaw, err := os.ReadFile(filepath.Join("..", "app", "configs", "workflows", "ichat_text_to_image.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg model.WorkflowConfig
	if err := yaml.Unmarshal(cfgRaw, &cfg); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(cfg.Required, "input_image") {
		t.Fatalf("text to image required = %v, want no input_image", cfg.Required)
	}

	workflowRaw, err := os.ReadFile(filepath.Join("..", "workflows", "ichat_text_to_image.json"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow map[string]map[string]any
	if err := json.Unmarshal(workflowRaw, &workflow); err != nil {
		t.Fatal(err)
	}
	hasSaveImage := false
	for id, node := range workflow {
		switch node["class_type"] {
		case "LoadImage", "GetImageSize":
			t.Fatalf("text to image workflow still has reference image node %s: %s", id, node["class_type"])
		case "SaveImage":
			hasSaveImage = true
		}
	}
	if !hasSaveImage {
		t.Fatal("text to image workflow must save the generated image")
	}
}
