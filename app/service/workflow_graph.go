package service

import (
	"encoding/json"
	"strconv"
	"strings"
)

func relinkNodeInputSourceForNodeIDsExcludingClass(
	prompt map[string]any,
	nodeIDs []string,
	fromNodeID string,
	fromSlot int,
	toNodeID string,
	toSlot int,
	excludeClass map[string]struct{},
) {
	for _, nodeID := range nodeIDs {
		rawNode, ok := prompt[nodeID]
		if !ok {
			continue
		}
		node, ok := rawNode.(map[string]any)
		if !ok {
			continue
		}
		classType := strings.TrimSpace(asString(node["class_type"]))
		if _, skip := excludeClass[classType]; skip {
			continue
		}
		inputs, ok := node["inputs"].(map[string]any)
		if !ok {
			continue
		}
		for inputName, rawInput := range inputs {
			sourceID, sourceSlot, ok := parseNodeLink(rawInput)
			if !ok {
				continue
			}
			if sourceID == fromNodeID && sourceSlot == fromSlot {
				inputs[inputName] = []any{toNodeID, toSlot}
			}
		}
	}
}

func parseNodeLink(v any) (string, int, bool) {
	link, ok := v.([]any)
	if !ok || len(link) != 2 {
		return "", 0, false
	}
	nodeID := asString(link[0])
	if nodeID == "" {
		return "", 0, false
	}
	slot := getInt(link[1], 0)
	return nodeID, slot, true
}

func maxNumericNodeID(prompt map[string]any) int {
	maxID := 0
	for key := range prompt {
		n, err := strconv.Atoi(strings.TrimSpace(key))
		if err != nil {
			continue
		}
		if n > maxID {
			maxID = n
		}
	}
	return maxID
}

func deepCopyNodeMap(src map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(src)
	if err != nil {
		return nil, err
	}
	var dst map[string]any
	if err := json.Unmarshal(raw, &dst); err != nil {
		return nil, err
	}
	return dst, nil
}
