package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

func loadWorkflows(configDir string) (map[string]WorkflowConfig, error) {
	entries, err := os.ReadDir(configDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]WorkflowConfig{}, nil
		}
		return nil, err
	}
	out := map[string]WorkflowConfig{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.ToLower(e.Name())
		if !(strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")) {
			continue
		}
		path := filepath.Join(configDir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var cfg WorkflowConfig
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("yaml parse failed (%s): %w", e.Name(), err)
		}
		cfg.ID = strings.TrimSpace(cfg.ID)
		cfg.Name = strings.TrimSpace(cfg.Name)
		if cfg.ID == "" {
			return nil, fmt.Errorf("workflow id missing in %s", e.Name())
		}
		if cfg.Template == "" {
			return nil, fmt.Errorf("workflow template missing in %s", e.Name())
		}
		cfg.ConfigFile = path
		if filepath.IsAbs(cfg.Template) {
			cfg.TemplatePath = cfg.Template
		} else {
			cfg.TemplatePath = filepath.Clean(filepath.Join(filepath.Dir(path), cfg.Template))
		}
		cfg.TemplateFormat = strings.TrimSpace(strings.ToLower(cfg.TemplateFormat))
		if cfg.TemplateFormat == "" {
			cfg.TemplateFormat = "auto"
		}
		if cfg.TemplateFormat != "auto" && cfg.TemplateFormat != "api" && cfg.TemplateFormat != "ui" {
			return nil, fmt.Errorf("invalid template_format in %s: %s", e.Name(), cfg.TemplateFormat)
		}
		if cfg.Defaults == nil {
			cfg.Defaults = map[string]any{}
		}
		if cfg.OutputKind == "" {
			cfg.OutputKind = "image"
		}
		if _, err := os.Stat(cfg.TemplatePath); err != nil {
			return nil, fmt.Errorf("template not found for workflow=%s: %s", cfg.ID, cfg.TemplatePath)
		}
		if _, exists := out[cfg.ID]; exists {
			return nil, fmt.Errorf("duplicate workflow id: %s", cfg.ID)
		}
		out[cfg.ID] = cfg
	}
	if len(out) == 0 {
		return out, nil
	}
	return out, nil
}

func defaultServiceConfig() ServiceConfig {
	return ServiceConfig{
		ExposeWorkflowCatalog: false,
		ExposeRawTaskCreate:   false,
		StorageMode:           "",
		DefaultWorkflows:      CategoryWorkflowConfig{},
	}
}

func loadServiceConfig(configPath string, workflows map[string]WorkflowConfig) (ServiceConfig, error) {
	cfg := defaultServiceConfig()
	raw, err := os.ReadFile(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, fmt.Errorf("service config not found: %s", configPath)
		}
		return cfg, err
	}

	var fileCfg ServiceConfig
	if err := yaml.Unmarshal(raw, &fileCfg); err != nil {
		return cfg, fmt.Errorf("service yaml parse failed: %w", err)
	}

	if strings.TrimSpace(fileCfg.DefaultWorkflows.TextToImage) != "" {
		cfg.DefaultWorkflows.TextToImage = strings.TrimSpace(fileCfg.DefaultWorkflows.TextToImage)
	}
	if strings.TrimSpace(fileCfg.DefaultWorkflows.ImageEdit) != "" {
		cfg.DefaultWorkflows.ImageEdit = strings.TrimSpace(fileCfg.DefaultWorkflows.ImageEdit)
	}
	if strings.TrimSpace(fileCfg.DefaultWorkflows.TextToVideo) != "" {
		cfg.DefaultWorkflows.TextToVideo = strings.TrimSpace(fileCfg.DefaultWorkflows.TextToVideo)
	}
	if strings.TrimSpace(fileCfg.DefaultWorkflows.ImageToVideo) != "" {
		cfg.DefaultWorkflows.ImageToVideo = strings.TrimSpace(fileCfg.DefaultWorkflows.ImageToVideo)
	}
	cfg.ExposeWorkflowCatalog = fileCfg.ExposeWorkflowCatalog
	cfg.ExposeRawTaskCreate = fileCfg.ExposeRawTaskCreate
	cfg.GatewaySecret = strings.TrimSpace(fileCfg.GatewaySecret)
	storageMode, err := NormalizeStorageMode(fileCfg.StorageMode)
	if err != nil {
		return cfg, err
	}
	cfg.StorageMode = storageMode
	cfg.S3 = fileCfg.S3
	cfg.Text = fileCfg.Text

	if len(workflows) == 0 {
		return cfg, nil
	}

	check := func(category, workflowID string) error {
		if workflowID == "" {
			return nil
		}
		if _, ok := workflows[workflowID]; !ok {
			return fmt.Errorf("service config category %s points to unknown workflow_id=%s", category, workflowID)
		}
		return nil
	}
	if err := check("text_to_image", cfg.DefaultWorkflows.TextToImage); err != nil {
		return cfg, err
	}
	if err := check("image_edit", cfg.DefaultWorkflows.ImageEdit); err != nil {
		return cfg, err
	}
	if err := check("text_to_video", cfg.DefaultWorkflows.TextToVideo); err != nil {
		return cfg, err
	}
	if err := check("image_to_video", cfg.DefaultWorkflows.ImageToVideo); err != nil {
		return cfg, err
	}
	return cfg, nil
}
