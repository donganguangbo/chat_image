package service

import (
	"fmt"
	"strconv"
	"strings"
)

type loraEntry struct {
	Name          string
	Attach        string // "stage1" | "stage2" | "both" (i2v); empty for image_edit/t2i
	StrengthModel float64
	StrengthClip  float64
}

func parseLoraEntries(raw any, mode string) ([]loraEntry, error) {
	arr, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("params.loras must be an array, got %T", raw)
	}
	out := make([]loraEntry, 0, len(arr))
	for i, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("params.loras[%d] must be an object, got %T", i, item)
		}
		name := strings.TrimSpace(asString(m["name"]))
		if name == "" {
			return nil, fmt.Errorf("params.loras[%d].name is required", i)
		}
		name = strings.TrimPrefix(name, "models/loras/")
		name = strings.TrimPrefix(name, "loras/")

		strength := getFloat64(m["strength"], 0.8)
		strengthModel := getFloat64(m["strength_model"], strength)
		strengthClip := getFloat64(m["strength_clip"], strength)

		entry := loraEntry{
			Name:          name,
			StrengthModel: strengthModel,
			StrengthClip:  strengthClip,
		}

		if mode == "i2v" {
			attach := strings.ToLower(strings.TrimSpace(asString(m["attach"])))
			switch attach {
			case "stage1", "stage2", "both":
				// ok
			case "":
				return nil, fmt.Errorf("params.loras[%d].attach is required for i2v mode (stage1|stage2|both)", i)
			default:
				return nil, fmt.Errorf("params.loras[%d].attach=%q invalid, allowed: stage1, stage2, both", i, attach)
			}
			entry.Attach = attach
		}

		out = append(out, entry)
	}
	return out, nil
}

func applyMultiLoraI2V(prompt map[string]any, params map[string]any, loras []loraEntry) error {
	stageNodeIDs, err := readLoraStageNodeIDs(params)
	if err != nil {
		return err
	}

	// Validate stage node IDs up front: any stage referenced by a LoRA entry must resolve
	// to a node that exists in the rendered workflow.
	needed := map[string]bool{}
	for _, e := range loras {
		switch e.Attach {
		case "stage1", "stage2":
			needed[e.Attach] = true
		case "both":
			needed["stage1"] = true
			needed["stage2"] = true
		}
	}
	for stage := range needed {
		baseNodeID := stageNodeIDs[stage]
		if baseNodeID == "" {
			return fmt.Errorf("i2v LoRA attach=%s requires workflow config lora_stage_node_ids.%s", stage, stage)
		}
		if _, ok := prompt[baseNodeID].(map[string]any); !ok {
			return fmt.Errorf("i2v LoRA %s base node %q not found in workflow", stage, baseNodeID)
		}
	}

	// Group LoRA entries by their *resolved* base node ID rather than by stage name, so
	// AIO workflows (where stage1 and stage2 alias to the same UNET) produce a single
	// chain instead of two competing ones.
	type stageGroup struct {
		baseNodeID string
		entries    []loraEntry
	}
	groupIndex := map[string]int{}
	groups := []stageGroup{}
	addEntry := func(stage string, entry loraEntry) {
		baseNodeID := stageNodeIDs[stage]
		if idx, ok := groupIndex[baseNodeID]; ok {
			groups[idx].entries = append(groups[idx].entries, entry)
			return
		}
		groupIndex[baseNodeID] = len(groups)
		groups = append(groups, stageGroup{baseNodeID: baseNodeID, entries: []loraEntry{entry}})
	}
	for _, e := range loras {
		switch e.Attach {
		case "stage1", "stage2":
			addEntry(e.Attach, e)
		case "both":
			addEntry("stage1", e)
			if stageNodeIDs["stage2"] != stageNodeIDs["stage1"] {
				addEntry("stage2", e)
			}
		}
	}

	// Snapshot the original node IDs once so later relinks target only pre-existing consumers,
	// not the LoRA nodes we're about to insert.
	originalNodeIDs := make([]string, 0, len(prompt))
	for id := range prompt {
		originalNodeIDs = append(originalNodeIDs, id)
	}

	for _, g := range groups {
		currentSource := g.baseNodeID
		for _, entry := range g.entries {
			newNodeID := strconv.Itoa(maxNumericNodeID(prompt) + 1)
			prompt[newNodeID] = map[string]any{
				"inputs": map[string]any{
					"model":          []any{currentSource, 0},
					"lora_name":      entry.Name,
					"strength_model": entry.StrengthModel,
				},
				"class_type": "LoraLoaderModelOnly",
				"_meta": map[string]any{
					"title": "LoraLoaderModelOnly",
				},
			}
			currentSource = newNodeID
		}
		if currentSource != g.baseNodeID {
			relinkNodeInputSourceForNodeIDsExcludingClass(prompt, originalNodeIDs, g.baseNodeID, 0, currentSource, 0, nil)
		}
	}
	return nil
}

func readLoraStageNodeIDs(params map[string]any) (map[string]string, error) {
	raw, ok := params["lora_stage_node_ids"]
	if !ok || raw == nil {
		return nil, fmt.Errorf("i2v LoRA request requires workflow config lora_stage_node_ids (e.g. {stage1: \"279\", stage2: \"280\"})")
	}
	out := map[string]string{}
	switch m := raw.(type) {
	case map[string]any:
		for k, v := range m {
			out[strings.ToLower(strings.TrimSpace(k))] = asString(v)
		}
	case map[any]any:
		for k, v := range m {
			out[strings.ToLower(strings.TrimSpace(asString(k)))] = asString(v)
		}
	default:
		return nil, fmt.Errorf("lora_stage_node_ids must be a map, got %T", raw)
	}
	return out, nil
}
