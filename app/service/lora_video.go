package service

import (
	"fmt"
	"log"
	"strconv"
	"strings"
)

func (s *Server) applyVideoActionLora(taskID string, prompt map[string]any, params map[string]any, workflow WorkflowConfig) error {
	dynamic := workflow.DynamicLora
	catalogID := strings.TrimSpace(dynamic.Catalog)
	if catalogID == "" {
		return nil
	}
	catalog, ok := s.service.VideoLoraCatalogs[catalogID]
	if !ok {
		return fmt.Errorf("video lora catalog not loaded: %s", catalogID)
	}
	actionID := strings.TrimSpace(asString(params["action_id"]))
	profileID := ""
	if catalog.Enabled && actionID != "" {
		profileID = strings.TrimSpace(catalog.Actions[actionID])
	}
	if profileID == "" {
		log.Printf("[video lora] task=%s workflow=%s action=%s profile=none", taskID, workflow.ID, logValue(actionID))
		return nil
	}
	profile, ok := catalog.Profiles[profileID]
	if !ok {
		return fmt.Errorf("video lora profile not loaded: %s/%s", catalogID, profileID)
	}
	bypassed := dynamic.BypassFixedLoras[actionID]
	if err := injectVideoLoraChain(prompt, strings.TrimSpace(dynamic.AnchorNode), profile.Loras); err != nil {
		return err
	}
	if err := bypassFixedVideoLoras(prompt, bypassed); err != nil {
		return err
	}
	loaded := make([]string, 0, len(profile.Loras))
	for _, spec := range profile.Loras {
		loaded = append(loaded, fmt.Sprintf("%s@%g", spec.File, spec.Strength))
	}
	log.Printf(
		"[video lora] task=%s workflow=%s action=%s profile=%s loras=%s bypassed=%s anchor=%s",
		taskID,
		workflow.ID,
		actionID,
		profileID,
		strings.Join(loaded, ","),
		strings.Join(bypassed, ","),
		dynamic.AnchorNode,
	)
	return nil
}

func bypassFixedVideoLoras(prompt map[string]any, files []string) error {
	for _, file := range files {
		file = strings.TrimSpace(file)
		nodeIDs := make([]string, 0, 1)
		for nodeID, rawNode := range prompt {
			node, ok := rawNode.(map[string]any)
			if !ok || asString(node["class_type"]) != "LoraLoaderModelOnly" {
				continue
			}
			inputs, ok := node["inputs"].(map[string]any)
			if ok && strings.TrimSpace(asString(inputs["lora_name"])) == file {
				nodeIDs = append(nodeIDs, nodeID)
			}
		}
		if len(nodeIDs) == 0 {
			return fmt.Errorf("fixed video lora not found: %s", file)
		}
		for _, nodeID := range nodeIDs {
			node := prompt[nodeID].(map[string]any)
			inputs := node["inputs"].(map[string]any)
			sourceID, sourceSlot, ok := parseNodeLink(inputs["model"])
			if !ok {
				return fmt.Errorf("fixed video lora %s node %s has invalid model input", file, nodeID)
			}
			consumerIDs := make([]string, 0, len(prompt))
			for consumerID := range prompt {
				consumerIDs = append(consumerIDs, consumerID)
			}
			relinkNodeInputSourceForNodeIDsExcludingClass(prompt, consumerIDs, nodeID, 0, sourceID, sourceSlot, nil)
			delete(prompt, nodeID)
		}
	}
	return nil
}

func injectVideoLoraChain(prompt map[string]any, anchorNodeID string, loras []VideoLoraSpec) error {
	if _, ok := prompt[anchorNodeID].(map[string]any); !ok {
		return fmt.Errorf("video lora anchor node not found: %s", anchorNodeID)
	}
	if len(loras) == 0 {
		return nil
	}
	originalNodeIDs := make([]string, 0, len(prompt))
	for nodeID := range prompt {
		originalNodeIDs = append(originalNodeIDs, nodeID)
	}
	currentSource := anchorNodeID
	for _, lora := range loras {
		newNodeID := strconv.Itoa(maxNumericNodeID(prompt) + 1)
		prompt[newNodeID] = map[string]any{
			"inputs": map[string]any{
				"model":          []any{currentSource, 0},
				"lora_name":      lora.File,
				"strength_model": lora.Strength,
			},
			"class_type": "LoraLoaderModelOnly",
			"_meta": map[string]any{
				"title": "Dynamic Video LoRA",
			},
		}
		currentSource = newNodeID
	}
	relinkNodeInputSourceForNodeIDsExcludingClass(prompt, originalNodeIDs, anchorNodeID, 0, currentSource, 0, nil)
	return nil
}

func logValue(value string) string {
	if value == "" {
		return "none"
	}
	return value
}
