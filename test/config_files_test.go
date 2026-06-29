package test

import (
	"os"
	"path/filepath"
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
