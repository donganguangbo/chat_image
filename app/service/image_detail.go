package service

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

var imageEditDetailTargetModels = map[string]string{
	"face":          "bbox/face_yolov8m.pt",
	"pussy":         "bbox/pussyV2.pt",
	"nipple":        "bbox/nipple.pt",
	"breast":        "bbox/female_breast_v3.pt",
	"female_breast": "bbox/female_breast_v3.pt",
	"penis":         "bbox/penisV2.pt",
}

func normalizeImageEditDetailParams(mode string, params map[string]any) error {
	if strings.TrimSpace(mode) != "image_edit" || params == nil {
		return nil
	}

	models := make([]string, 0, 6)
	seen := map[string]struct{}{}
	addModel := func(model string) {
		m := normalizeBBoxModelPath(model)
		if m == "" {
			return
		}
		if _, ok := seen[m]; ok {
			return
		}
		seen[m] = struct{}{}
		models = append(models, m)
	}
	addTarget := func(target string) error {
		t := strings.ToLower(strings.TrimSpace(target))
		if t == "" {
			return nil
		}
		model, ok := imageEditDetailTargetModels[t]
		if !ok {
			return fmt.Errorf("invalid params.detail_target=%q, allowed: %s", t, strings.Join(allowedDetailTargets(), ", "))
		}
		addModel(model)
		return nil
	}

	for _, target := range asStringSlice(params["detail_targets"]) {
		if err := addTarget(target); err != nil {
			return err
		}
	}
	if target := asString(params["detail_target"]); target != "" {
		if err := addTarget(target); err != nil {
			return err
		}
	}

	for _, model := range asStringSlice(params["detail_bbox_models"]) {
		addModel(model)
	}
	if model := asString(params["detail_bbox_model"]); model != "" {
		addModel(model)
	}

	if len(models) == 0 {
		return nil
	}
	params["detail_bbox_model"] = models[0]
	modelsAny := make([]any, 0, len(models))
	for _, model := range models {
		modelsAny = append(modelsAny, model)
	}
	params["detail_bbox_models"] = modelsAny
	return nil
}

func allowedDetailTargets() []string {
	out := make([]string, 0, len(imageEditDetailTargetModels))
	for k := range imageEditDetailTargetModels {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func asStringSlice(v any) []string {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		return splitCSVLike(x)
	case []string:
		out := make([]string, 0, len(x))
		for _, item := range x {
			s := strings.TrimSpace(item)
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			s := asString(item)
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		s := asString(v)
		if s == "" {
			return nil
		}
		return splitCSVLike(s)
	}
}

func splitCSVLike(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t'
	})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		s := strings.TrimSpace(part)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func normalizeBBoxModelPath(model string) string {
	s := strings.TrimSpace(model)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "/") {
		return "bbox/" + s
	}
	return s
}

func applyInpaintMultiDetail(prompt map[string]any, params map[string]any, refineEnabled bool) error {
	if !refineEnabled || prompt == nil {
		return nil
	}

	models := make([]string, 0, 6)
	seen := map[string]struct{}{}
	for _, model := range asStringSlice(params["detail_bbox_models"]) {
		m := normalizeBBoxModelPath(model)
		if m == "" {
			continue
		}
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		models = append(models, m)
	}
	if len(models) == 0 {
		if single := normalizeBBoxModelPath(asString(params["detail_bbox_model"])); single != "" {
			models = append(models, single)
		}
	}
	if len(models) <= 1 {
		return nil
	}

	baseDetector, ok := prompt["61"].(map[string]any)
	if !ok {
		return errors.New("workflow missing node 61 (UltralyticsDetectorProvider)")
	}
	baseDetectorInputs, ok := baseDetector["inputs"].(map[string]any)
	if !ok {
		return errors.New("workflow node 61 inputs invalid")
	}
	baseDetectorInputs["model_name"] = models[0]

	baseDetailer, ok := prompt["60"].(map[string]any)
	if !ok {
		return errors.New("workflow missing node 60 (FaceDetailer)")
	}
	if _, ok := baseDetailer["inputs"].(map[string]any); !ok {
		return errors.New("workflow node 60 inputs invalid")
	}

	originalNodeIDs := make([]string, 0, len(prompt))
	for id := range prompt {
		originalNodeIDs = append(originalNodeIDs, id)
	}

	nextID := maxNumericNodeID(prompt) + 1
	previousDetailerID := "60"
	for _, model := range models[1:] {
		detectorClone, err := deepCopyNodeMap(baseDetector)
		if err != nil {
			return fmt.Errorf("clone detector failed: %w", err)
		}
		detectorInputs, _ := detectorClone["inputs"].(map[string]any)
		detectorInputs["model_name"] = model
		detectorID := strconv.Itoa(nextID)
		nextID++
		prompt[detectorID] = detectorClone

		detailerClone, err := deepCopyNodeMap(baseDetailer)
		if err != nil {
			return fmt.Errorf("clone detailer failed: %w", err)
		}
		detailerInputs, _ := detailerClone["inputs"].(map[string]any)
		detailerInputs["bbox_detector"] = []any{detectorID, 0}
		detailerInputs["image"] = []any{previousDetailerID, 0}
		detailerID := strconv.Itoa(nextID)
		nextID++
		prompt[detailerID] = detailerClone

		previousDetailerID = detailerID
	}

	relinkNodeInputSourceForNodeIDsExcludingClass(prompt, originalNodeIDs, "60", 0, previousDetailerID, 0, nil)
	return nil
}

func applyInpaintRefineSwitch(prompt map[string]any, refineEnabled bool) {
	// Keep default chain when refine is enabled.
	if refineEnabled {
		return
	}
	// Match Python gateway behavior:
	// - route final SaveImage to ReActor output (node 50)
	// - drop extra after_swap save node (node 80)
	if nodeAfter, ok := prompt["8"].(map[string]any); ok {
		if inputs, ok := nodeAfter["inputs"].(map[string]any); ok {
			inputs["images"] = []any{"50", 0}
		}
	}
	delete(prompt, "80")
}
