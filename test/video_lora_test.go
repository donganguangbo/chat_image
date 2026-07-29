package test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"chat_image/app/service"
)

func TestVideoLoraInjectionUsesActionID(t *testing.T) {
	t.Run("text to video chains configured loras", func(t *testing.T) {
		prompt := captureVideoPrompt(t, "text_to_video", "285", "cumshot", []service.VideoLoraSpec{
			{File: "action.safetensors", Strength: 0.8},
			{File: "expression.safetensors", Strength: 0.6},
		}, "CUMSH0T", nil)
		assertModelLink(t, prompt, "317", "285")
		assertModelLink(t, prompt, "318", "317")
		assertModelLink(t, prompt, "282", "318")
		assertModelLink(t, prompt, "316", "318")
		assertLora(t, prompt, "317", "action.safetensors", 0.8)
		assertLora(t, prompt, "318", "expression.safetensors", 0.6)
		assertStringInput(t, prompt, "303", "value", "CUMSH0T. original motion")
	})

	t.Run("image to video injects before fixed lora", func(t *testing.T) {
		prompt := captureVideoPrompt(t, "i2v", "285", "cumshot", []service.VideoLoraSpec{
			{File: "action.safetensors", Strength: 0.8},
		}, "CUMSH0T", nil)
		assertModelLink(t, prompt, "330", "285")
		assertModelLink(t, prompt, "329", "330")
		assertModelLink(t, prompt, "282", "329")
		assertModelLink(t, prompt, "316", "329")
	})

	t.Run("image to video can bypass fixed motion lora", func(t *testing.T) {
		prompt := captureVideoPrompt(t, "i2v", "285", "missionary-cumshot", []service.VideoLoraSpec{
			{File: "action.safetensors", Strength: 1},
		}, "CUMSH0T", map[string][]string{
			"missionary-cumshot": {"fixed.safetensors"},
		})
		if _, exists := prompt["329"]; exists {
			t.Fatal("fixed motion lora node 329 must be bypassed")
		}
		assertModelLink(t, prompt, "330", "285")
		assertModelLink(t, prompt, "282", "330")
		assertModelLink(t, prompt, "316", "330")
	})

	for _, actionID := range []string{"", "unmapped-action"} {
		t.Run("no dynamic lora for "+actionID, func(t *testing.T) {
			prompt := captureVideoPrompt(t, "text_to_video", "285", actionID, []service.VideoLoraSpec{
				{File: "action.safetensors", Strength: 0.8},
			}, "CUMSH0T", nil)
			if _, exists := prompt["317"]; exists {
				t.Fatalf("unexpected dynamic lora node for action %q", actionID)
			}
			assertModelLink(t, prompt, "282", "285")
			assertModelLink(t, prompt, "316", "285")
			assertStringInput(t, prompt, "303", "value", "original motion")
		})
	}
}

func captureVideoPrompt(t *testing.T, mode, anchorNode, actionID string, loras []service.VideoLoraSpec, promptPrefix string, bypassFixedLoras map[string][]string) map[string]any {
	t.Helper()
	queued := make(chan map[string]any, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/prompt":
			var body struct {
				Prompt map[string]any `json:"prompt"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			queued <- body.Prompt
			_ = json.NewEncoder(w).Encode(map[string]string{"prompt_id": "captured"})
		case r.Method == http.MethodGet && r.URL.Path == "/history/captured":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"captured": map[string]any{
					"status": map[string]any{
						"status_str": "error",
						"messages":   []any{[]any{"execution_error", map[string]any{"exception_message": "captured by test"}}},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer backend.Close()

	guiderSource := anchorNode
	if mode == "i2v" {
		guiderSource = "329"
	}
	template := map[string]any{
		"281": map[string]any{"class_type": "CheckpointLoaderSimple", "inputs": map[string]any{}},
		"285": map[string]any{
			"class_type": "LoraLoaderModelOnly",
			"inputs":     map[string]any{"model": []any{"281", 0}, "lora_name": "distilled.safetensors", "strength_model": 0.5},
		},
		"282": map[string]any{"class_type": "CFGGuider", "inputs": map[string]any{"model": []any{guiderSource, 0}}},
		"303": map[string]any{"class_type": "PrimitiveStringMultiline", "inputs": map[string]any{"value": "template motion"}},
		"316": map[string]any{"class_type": "CFGGuider", "inputs": map[string]any{"model": []any{guiderSource, 0}}},
	}
	if mode == "i2v" {
		template["325"] = map[string]any{
			"class_type": "LoadImage",
			"inputs":     map[string]any{"image": "partner.png"},
		}
		template["327"] = map[string]any{
			"class_type": "FirstFrame",
			"inputs":     map[string]any{"image2": []any{"325", 0}},
		}
		template["329"] = map[string]any{
			"class_type": "LoraLoaderModelOnly",
			"inputs":     map[string]any{"model": []any{"285", 0}, "lora_name": "fixed.safetensors", "strength_model": 1.0},
		}
	}
	templateRaw, err := json.Marshal(template)
	if err != nil {
		t.Fatal(err)
	}
	templatePath := filepath.Join(t.TempDir(), "workflow.json")
	if err := os.WriteFile(templatePath, templateRaw, 0o600); err != nil {
		t.Fatal(err)
	}

	workflowID := "video"
	server := service.NewServer(
		map[string]service.WorkflowConfig{
			workflowID: {
				ID:             workflowID,
				TemplatePath:   templatePath,
				TemplateFormat: "api",
				Mode:           mode,
				OutputKind:     "video",
				TimeoutSec:     5,
				Bindings: []service.InputBinding{
					{Node: "303", Input: "value", Param: "motion_prompt"},
				},
				DynamicLora: service.VideoLoraWorkflowConfig{
					Catalog:          "ltx_2_3",
					AnchorNode:       anchorNode,
					BypassFixedLoras: bypassFixedLoras,
				},
			},
		},
		service.NewComfyPool([]string{backend.URL}),
		service.ServiceConfig{
			VideoLoraCatalogs: map[string]service.VideoLoraCatalog{
				"ltx_2_3": {
					ID:      "ltx_2_3",
					Enabled: true,
					Profiles: map[string]service.VideoLoraProfile{
						"test": {PromptPrefix: promptPrefix, Loras: loras},
					},
					Actions: map[string]string{"cumshot": "test", "missionary-cumshot": "test"},
				},
			},
		},
		1,
		t.TempDir(),
		nil,
	)
	params := map[string]any{"motion_prompt": "original motion"}
	if actionID != "" {
		params["action_id"] = actionID
	}
	task, err := server.CreateTask(service.CreateTaskRequest{WorkflowID: workflowID, Prompt: "test", Params: params})
	if err != nil {
		t.Fatal(err)
	}

	var prompt map[string]any
	select {
	case prompt = <-queued:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for queued workflow")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if current := server.GetTask(task.ID); current != nil && current.Status == "failed" {
			return prompt
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("task did not finish after prompt capture: %+v", server.GetTask(task.ID))
	return nil
}

func assertModelLink(t *testing.T, prompt map[string]any, nodeID, sourceID string) {
	t.Helper()
	node, ok := prompt[nodeID].(map[string]any)
	if !ok {
		t.Fatalf("node %s missing", nodeID)
	}
	inputs, ok := node["inputs"].(map[string]any)
	if !ok {
		t.Fatalf("node %s inputs missing", nodeID)
	}
	link, ok := inputs["model"].([]any)
	if !ok || len(link) != 2 || link[0] != sourceID {
		t.Fatalf("node %s model = %#v, want source %s", nodeID, inputs["model"], sourceID)
	}
}

func assertLora(t *testing.T, prompt map[string]any, nodeID, file string, strength float64) {
	t.Helper()
	node := prompt[nodeID].(map[string]any)
	inputs := node["inputs"].(map[string]any)
	if inputs["lora_name"] != file || inputs["strength_model"] != strength {
		t.Fatalf("node %s lora inputs = %+v", nodeID, inputs)
	}
}

func assertStringInput(t *testing.T, prompt map[string]any, nodeID, input, want string) {
	t.Helper()
	node := prompt[nodeID].(map[string]any)
	inputs := node["inputs"].(map[string]any)
	if got := inputs[input]; got != want {
		t.Fatalf("node %s input %s = %q, want %q", nodeID, input, got, want)
	}
}
