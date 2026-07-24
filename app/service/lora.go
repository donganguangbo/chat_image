package service

import (
	"fmt"
	"log"
	"strconv"
	"strings"
)

func applyOptionalLora(prompt map[string]any, params map[string]any, mode string) error {
	if prompt == nil || params == nil {
		return nil
	}

	// New multi-LoRA path: params.loras array takes precedence over legacy single-slot fields.
	if rawLoras, ok := params["loras"]; ok && rawLoras != nil {
		loras, err := parseLoraEntries(rawLoras)
		if err != nil {
			return err
		}
		if len(loras) == 0 {
			return nil
		}
		if _, hasOld := params["lora_name"]; hasOld {
			log.Printf("applyOptionalLora: both params.loras and params.lora_name present; loras[] wins, lora_name ignored")
		}
		if getBool(params["lora_enabled"], false) {
			log.Printf("applyOptionalLora: both params.loras and params.lora_enabled=true present; loras[] wins, lora_enabled ignored")
		}
		// image_edit / t2i keep legacy single-slot semantics and use the first entry.
		return applyLegacyLoraFromMultiEntry(prompt, params, mode, loras[0])
	}

	// Legacy single-slot path — preserved bit-for-bit for image_edit / t2i backward compatibility.
	return applyLegacyLora(prompt, params, mode)
}

func applyLegacyLora(prompt map[string]any, params map[string]any, mode string) error {
	enabled := getBool(params["lora_enabled"], false)
	if !enabled {
		return nil
	}

	loraName := strings.TrimSpace(asString(params["lora_name"]))
	if loraName == "" {
		return nil
	}
	loraName = strings.TrimPrefix(loraName, "models/loras/")
	loraName = strings.TrimPrefix(loraName, "loras/")

	baseNodeID := asString(params["lora_base_node_id"])
	if baseNodeID == "" {
		baseNodeID = "69"
	}
	baseNode, ok := prompt[baseNodeID].(map[string]any)
	if !ok {
		return fmt.Errorf("lora base node not found: %s", baseNodeID)
	}
	baseClassType := asString(baseNode["class_type"])
	if baseClassType == "" {
		return fmt.Errorf("lora base node class_type missing: %s", baseNodeID)
	}

	modelOnly := getBool(params["lora_model_only"], false)
	strengthModel := getFloat64(params["lora_strength_model"], 0.8)
	strengthClip := getFloat64(params["lora_strength_clip"], strengthModel)

	originalNodeIDs := make([]string, 0, len(prompt))
	for id := range prompt {
		originalNodeIDs = append(originalNodeIDs, id)
	}

	newNodeID := strconv.Itoa(maxNumericNodeID(prompt) + 1)
	if modelOnly {
		prompt[newNodeID] = map[string]any{
			"inputs": map[string]any{
				"model":          []any{baseNodeID, 0},
				"lora_name":      loraName,
				"strength_model": strengthModel,
			},
			"class_type": "LoraLoaderModelOnly",
			"_meta": map[string]any{
				"title": "LoraLoaderModelOnly",
			},
		}
	} else {
		prompt[newNodeID] = map[string]any{
			"inputs": map[string]any{
				"model":          []any{baseNodeID, 0},
				"clip":           []any{baseNodeID, 1},
				"lora_name":      loraName,
				"strength_model": strengthModel,
				"strength_clip":  strengthClip,
			},
			"class_type": "LoraLoader",
			"_meta": map[string]any{
				"title": "LoraLoader",
			},
		}
	}

	scope := strings.ToLower(strings.TrimSpace(asString(params["lora_scope"])))
	if scope == "" && strings.TrimSpace(mode) == "image_edit" {
		// For image_edit flows, default to applying LoRA on the initial generation chain
		// and keep face-detail/refine branch on base model to avoid pose drift.
		scope = "before_only"
	}
	if scope == "before_only" && strings.TrimSpace(mode) == "image_edit" {
		excludeClass := map[string]struct{}{
			"FaceDetailer": {},
		}
		relinkNodeInputSourceForNodeIDsExcludingClass(prompt, originalNodeIDs, baseNodeID, 0, newNodeID, 0, excludeClass)
		if !modelOnly {
			relinkNodeInputSourceForNodeIDsExcludingClass(prompt, originalNodeIDs, baseNodeID, 1, newNodeID, 1, excludeClass)
		}
	} else {
		relinkNodeInputSourceForNodeIDsExcludingClass(prompt, originalNodeIDs, baseNodeID, 0, newNodeID, 0, nil)
		if !modelOnly {
			relinkNodeInputSourceForNodeIDsExcludingClass(prompt, originalNodeIDs, baseNodeID, 1, newNodeID, 1, nil)
		}
	}
	return nil
}

// applyLegacyLoraFromMultiEntry lets image_edit / t2i callers send params.loras[] too,
// by mapping the first entry onto the existing single-slot fields and reusing the
// legacy code path (so FaceDetailer scope, CLIP wiring, etc. stay identical).
func applyLegacyLoraFromMultiEntry(prompt map[string]any, params map[string]any, mode string, entry loraEntry) error {
	shim := cloneMap(params)
	shim["lora_enabled"] = true
	shim["lora_name"] = entry.Name
	shim["lora_strength_model"] = entry.StrengthModel
	shim["lora_strength_clip"] = entry.StrengthClip
	// Preserve existing lora_base_node_id / lora_scope / lora_model_only if caller set them.
	return applyLegacyLora(prompt, shim, mode)
}
