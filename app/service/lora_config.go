package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	videoLoraCatalogIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,63}$`)
	videoLoraActionIDPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
)

func loadVideoLoraCatalogs(configDir string, workflows map[string]WorkflowConfig) (map[string]VideoLoraCatalog, error) {
	catalogs := map[string]VideoLoraCatalog{}
	entries, err := os.ReadDir(configDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := strings.ToLower(entry.Name())
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		configPath := filepath.Join(configDir, entry.Name())
		raw, err := os.ReadFile(configPath)
		if err != nil {
			return nil, err
		}
		var catalog VideoLoraCatalog
		if err := yaml.Unmarshal(raw, &catalog); err != nil {
			return nil, fmt.Errorf("video lora yaml parse failed (%s): %w", entry.Name(), err)
		}
		if err := validateVideoLoraCatalog(&catalog); err != nil {
			return nil, fmt.Errorf("invalid video lora catalog %s: %w", entry.Name(), err)
		}
		if _, exists := catalogs[catalog.ID]; exists {
			return nil, fmt.Errorf("duplicate video lora catalog id: %s", catalog.ID)
		}
		catalogs[catalog.ID] = catalog
	}
	if err := validateWorkflowVideoLoraConfigs(workflows, catalogs); err != nil {
		return nil, err
	}
	return catalogs, nil
}

func validateVideoLoraCatalog(catalog *VideoLoraCatalog) error {
	catalog.ID = strings.TrimSpace(catalog.ID)
	if !videoLoraCatalogIDPattern.MatchString(catalog.ID) {
		return fmt.Errorf("invalid id %q", catalog.ID)
	}
	if len(catalog.Profiles) == 0 {
		return errors.New("profiles must not be empty")
	}
	for profileID, profile := range catalog.Profiles {
		if !videoLoraCatalogIDPattern.MatchString(profileID) {
			return fmt.Errorf("invalid profile id %q", profileID)
		}
		if len(profile.Loras) == 0 {
			return fmt.Errorf("profile %s has no loras", profileID)
		}
		for i := range profile.Loras {
			spec := &profile.Loras[i]
			spec.File = strings.TrimSpace(spec.File)
			if !safeVideoLoraFile(spec.File) {
				return fmt.Errorf("profile %s loras[%d] has unsafe file %q", profileID, i, spec.File)
			}
			if math.IsNaN(spec.Strength) || math.IsInf(spec.Strength, 0) || spec.Strength <= 0 || spec.Strength > 2 {
				return fmt.Errorf("profile %s loras[%d] strength must be > 0 and <= 2", profileID, i)
			}
		}
		catalog.Profiles[profileID] = profile
	}
	for actionID, profileID := range catalog.Actions {
		if !videoLoraActionIDPattern.MatchString(actionID) {
			return fmt.Errorf("invalid action id %q", actionID)
		}
		profileID = strings.TrimSpace(profileID)
		if _, ok := catalog.Profiles[profileID]; !ok {
			return fmt.Errorf("action %s references unknown profile %q", actionID, profileID)
		}
		catalog.Actions[actionID] = profileID
	}
	return nil
}

func safeVideoLoraFile(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, `\`) {
		return false
	}
	clean := path.Clean(name)
	return clean == name && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}

func validateWorkflowVideoLoraConfigs(workflows map[string]WorkflowConfig, catalogs map[string]VideoLoraCatalog) error {
	for _, workflow := range workflows {
		dynamic := workflow.DynamicLora
		dynamic.Catalog = strings.TrimSpace(dynamic.Catalog)
		dynamic.AnchorNode = strings.TrimSpace(dynamic.AnchorNode)
		if dynamic.Catalog == "" && dynamic.AnchorNode == "" {
			continue
		}
		if workflow.Mode != "text_to_video" && workflow.Mode != "i2v" {
			return fmt.Errorf("workflow %s configures dynamic_lora for unsupported mode %s", workflow.ID, workflow.Mode)
		}
		if dynamic.Catalog == "" || dynamic.AnchorNode == "" {
			return fmt.Errorf("workflow %s dynamic_lora requires catalog and anchor_node", workflow.ID)
		}
		if _, ok := catalogs[dynamic.Catalog]; !ok {
			return fmt.Errorf("workflow %s references unknown video lora catalog %s", workflow.ID, dynamic.Catalog)
		}
		raw, err := os.ReadFile(workflow.TemplatePath)
		if err != nil {
			return fmt.Errorf("read workflow %s for dynamic_lora validation: %w", workflow.ID, err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return fmt.Errorf("parse workflow %s for dynamic_lora validation: %w", workflow.ID, err)
		}
		prompt, ok := extractAPIWorkflow(parsed)
		if !ok {
			return fmt.Errorf("workflow %s dynamic_lora requires an API workflow template", workflow.ID)
		}
		if _, ok := prompt[dynamic.AnchorNode].(map[string]any); !ok {
			return fmt.Errorf("workflow %s dynamic_lora anchor node %s not found", workflow.ID, dynamic.AnchorNode)
		}
		catalog := catalogs[dynamic.Catalog]
		for actionID, files := range dynamic.BypassFixedLoras {
			if !videoLoraActionIDPattern.MatchString(actionID) {
				return fmt.Errorf("workflow %s dynamic_lora has invalid bypass action %q", workflow.ID, actionID)
			}
			if _, ok := catalog.Actions[actionID]; !ok {
				return fmt.Errorf("workflow %s dynamic_lora bypass action %s is not mapped in catalog %s", workflow.ID, actionID, dynamic.Catalog)
			}
			if len(files) == 0 {
				return fmt.Errorf("workflow %s dynamic_lora bypass action %s has no loras", workflow.ID, actionID)
			}
			for _, file := range files {
				file = strings.TrimSpace(file)
				if !safeVideoLoraFile(file) {
					return fmt.Errorf("workflow %s dynamic_lora bypass action %s has unsafe file %q", workflow.ID, actionID, file)
				}
				if !hasFixedVideoLora(prompt, file) {
					return fmt.Errorf("workflow %s dynamic_lora bypass action %s references missing fixed lora %s", workflow.ID, actionID, file)
				}
			}
		}
	}
	return nil
}

func hasFixedVideoLora(prompt map[string]any, file string) bool {
	for _, rawNode := range prompt {
		node, ok := rawNode.(map[string]any)
		if !ok || asString(node["class_type"]) != "LoraLoaderModelOnly" {
			continue
		}
		inputs, ok := node["inputs"].(map[string]any)
		if ok && strings.TrimSpace(asString(inputs["lora_name"])) == file {
			return true
		}
	}
	return false
}
