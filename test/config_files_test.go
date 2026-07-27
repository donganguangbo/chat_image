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
	if !hasBinding(cfg, "25", "width", "width") || !hasBinding(cfg, "25", "height", "height") {
		t.Fatalf("image edit bindings must pass requested width/height into node 25: %+v", cfg.Bindings)
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
	latent, ok := workflow["25"]["inputs"].(map[string]any)
	if !ok {
		t.Fatal("image edit workflow node 25 inputs missing")
	}
	if latent["width"] != float64(576) || latent["height"] != float64(720) {
		t.Fatalf("image edit workflow node 25 default size = %vx%v, want 576x720", latent["width"], latent["height"])
	}
}

func hasBinding(cfg model.WorkflowConfig, nodeID string, input string, param string) bool {
	for _, binding := range cfg.Bindings {
		if binding.Node == nodeID && binding.Input == input && binding.Param == param {
			return true
		}
	}
	return false
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
	for _, want := range []struct {
		node, input, param string
	}{
		{"115", "text", "prompt"},
		{"116", "seed", "seed"},
		{"116", "steps", "steps"},
		{"116", "cfg", "cfg"},
		{"11", "width", "width"},
		{"11", "height", "height"},
	} {
		if !hasBinding(cfg, want.node, want.input, want.param) {
			t.Fatalf("text to image missing binding %s.%s <- %s", want.node, want.input, want.param)
		}
	}

	workflowRaw, err := os.ReadFile(filepath.Join("..", "workflows", "ichat_text_to_image.json"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow map[string]map[string]any
	if err := json.Unmarshal(workflowRaw, &workflow); err != nil {
		t.Fatal(err)
	}
	wantNodes := map[string]string{
		"2":   "UNETLoader",
		"4":   "ComfyUI-Krea2T-Enhancer",
		"5":   "VAELoader",
		"6":   "CLIPLoader",
		"11":  "EmptyLatentImage",
		"113": "VAEDecode",
		"114": "ConditioningZeroOut",
		"115": "CLIPTextEncode",
		"116": "ClownsharKSampler_Beta",
		"158": "SaveImage",
	}
	if len(workflow) != len(wantNodes) {
		t.Fatalf("text to image workflow has %d nodes, want %d", len(workflow), len(wantNodes))
	}
	for id, node := range workflow {
		switch node["class_type"] {
		case "LoadImage", "GetImageSize":
			t.Fatalf("text to image workflow still has reference image node %s: %s", id, node["class_type"])
		}
		if node["class_type"] != wantNodes[id] {
			t.Fatalf("text to image node %s class = %v, want %q", id, node["class_type"], wantNodes[id])
		}
		inputs, ok := node["inputs"].(map[string]any)
		if !ok {
			t.Fatalf("text to image node %s inputs missing", id)
		}
		for input, value := range inputs {
			link, ok := value.([]any)
			if !ok || len(link) != 2 {
				continue
			}
			source, ok := link[0].(string)
			if !ok {
				continue
			}
			if _, ok := workflow[source]; !ok {
				t.Fatalf("text to image node %s input %s links missing node %s", id, input, source)
			}
		}
	}
	unet := workflow["2"]["inputs"].(map[string]any)
	if unet["unet_name"] != "krea2_turbo_fp8_scaled.safetensors" {
		t.Fatalf("text to image model = %v", unet["unet_name"])
	}
}

func TestVideoWorkflowsUseExplicitStageBindings(t *testing.T) {
	workflowsDir := filepath.Join("..", "app", "configs", "workflows")

	i2vRaw, err := os.ReadFile(filepath.Join(workflowsDir, "ichat_image_to_video.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var i2v model.WorkflowConfig
	if err := yaml.Unmarshal(i2vRaw, &i2v); err != nil {
		t.Fatal(err)
	}
	if i2v.DynamicLora.Catalog != "ltx_2_3" || i2v.DynamicLora.AnchorNode != "285" {
		t.Fatalf("i2v dynamic_lora = %+v", i2v.DynamicLora)
	}
	for _, required := range []string{"motion_prompt", "motion_negative_prompt", "first_frame_prompt", "first_frame_negative_prompt", "source_image"} {
		if !slices.Contains(i2v.Required, required) {
			t.Fatalf("i2v required = %v, missing %s", i2v.Required, required)
		}
	}
	for _, binding := range []struct{ node, input, param string }{
		{"303", "value", "motion_prompt"},
		{"315", "text", "motion_negative_prompt"},
		{"328", "value", "first_frame_prompt"},
		{"323", "prompt", "first_frame_negative_prompt"},
		{"326", "image", "source_image"},
		{"325", "image", "partner_identity_image"},
	} {
		if !hasBinding(i2v, binding.node, binding.input, binding.param) {
			t.Fatalf("i2v missing binding %s.%s <- %s", binding.node, binding.input, binding.param)
		}
	}

	t2vRaw, err := os.ReadFile(filepath.Join(workflowsDir, "ichat_text_to_video.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var t2v model.WorkflowConfig
	if err := yaml.Unmarshal(t2vRaw, &t2v); err != nil {
		t.Fatal(err)
	}
	if t2v.DynamicLora.Catalog != "ltx_2_3" || t2v.DynamicLora.AnchorNode != "285" {
		t.Fatalf("t2v dynamic_lora = %+v", t2v.DynamicLora)
	}
	if !slices.Equal(t2v.Required, []string{"motion_prompt", "motion_negative_prompt"}) {
		t.Fatalf("t2v required = %v", t2v.Required)
	}
	if !hasBinding(t2v, "303", "value", "motion_prompt") || !hasBinding(t2v, "315", "text", "motion_negative_prompt") {
		t.Fatalf("t2v bindings = %+v", t2v.Bindings)
	}

	catalogRaw, err := os.ReadFile(filepath.Join("..", "app", "configs", "loras", "ltx_2_3.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var catalog model.VideoLoraCatalog
	if err := yaml.Unmarshal(catalogRaw, &catalog); err != nil {
		t.Fatal(err)
	}
	if !catalog.Enabled || catalog.Actions["cumshot"] != "epic_cumshots_v1" || catalog.Actions["missionary-cumshot"] != "epic_cumshots_v1" {
		t.Fatalf("video lora actions = %+v", catalog.Actions)
	}
	profile := catalog.Profiles["epic_cumshots_v1"]
	if profile.PromptPrefix != "CUMSH0T" || len(profile.Loras) != 1 || profile.Loras[0].File != "epic-cumshots-ltx-2.3.safetensors" || profile.Loras[0].Strength != 1 {
		t.Fatalf("epic_cumshots_v1 profile = %+v", profile)
	}
}
