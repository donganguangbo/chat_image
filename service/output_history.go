package service

import (
	"fmt"
	"sort"
	"strings"
)

func summarizeOutputNodes(outputsAny map[string]any) string {
	if len(outputsAny) == 0 {
		return "none"
	}
	ids := make([]string, 0, len(outputsAny))
	for id := range outputsAny {
		ids = append(ids, strings.TrimSpace(id))
	}
	sort.Strings(ids)
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		nodeMap, ok := outputsAny[id].(map[string]any)
		if !ok {
			parts = append(parts, id+":invalid")
			continue
		}
		items, _ := nodeMap["images"].([]any)
		if len(items) == 0 {
			parts = append(parts, id+":no_images")
			continue
		}
		first, _ := items[0].(map[string]any)
		filename := asString(first["filename"])
		subfolder := asString(first["subfolder"])
		parts = append(parts, fmt.Sprintf("%s:%s|%s", id, filename, subfolder))
	}
	return strings.Join(parts, ";")
}

func summarizeExpectedOutputNodes(outputsAny map[string]any, expectedIDs []string) string {
	if len(expectedIDs) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(expectedIDs))
	for _, id := range expectedIDs {
		nodeMapAny, ok := outputsAny[id]
		if !ok {
			parts = append(parts, id+":absent")
			continue
		}
		nodeMap, ok := nodeMapAny.(map[string]any)
		if !ok {
			parts = append(parts, id+":invalid")
			continue
		}
		items, _ := nodeMap["images"].([]any)
		if len(items) == 0 {
			parts = append(parts, id+":no_images")
			continue
		}
		first, _ := items[0].(map[string]any)
		filename := asString(first["filename"])
		subfolder := asString(first["subfolder"])
		parts = append(parts, fmt.Sprintf("%s:%s|%s", id, filename, subfolder))
	}
	return strings.Join(parts, ";")
}

func collectImageEditAfterNodeIDs(prompt map[string]any) []string {
	if prompt == nil {
		return nil
	}
	out := make([]string, 0, 2)
	for nodeID, raw := range prompt {
		node, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if asString(node["class_type"]) != "SaveImage" {
			continue
		}
		inputs, ok := node["inputs"].(map[string]any)
		if !ok {
			continue
		}
		prefix := strings.ToLower(asString(inputs["filename_prefix"]))
		if strings.Contains(prefix, "_after") &&
			!strings.Contains(prefix, "_after_swap") &&
			!strings.Contains(prefix, "_before") {
			out = append(out, strings.TrimSpace(nodeID))
		}
	}
	sort.Strings(out)
	return out
}

func findHistoryEntry(history map[string]any, promptID string) map[string]any {
	if v, ok := history[promptID].(map[string]any); ok {
		return v
	}
	return nil
}

func extractComfyHistoryError(historyEntry map[string]any) string {
	status, ok := historyEntry["status"].(map[string]any)
	if ok {
		if msg := extractComfyStatusMessagesError(status["messages"]); msg != "" {
			return msg
		}
		statusStr := strings.ToLower(strings.TrimSpace(asString(status["status_str"])))
		if statusStr == "" || statusStr == "success" || statusStr == "completed" {
			return ""
		}
		return "status=" + statusStr
	}
	if msg := extractComfyStatusMessagesError(historyEntry["messages"]); msg != "" {
		return msg
	}
	return ""
}

func extractComfyStatusMessagesError(rawMsgs any) string {
	msgs, ok := rawMsgs.([]any)
	if !ok {
		return ""
	}
	// Prefer explicit execution_error / prompt_outputs_failed_validation details.
	for _, raw := range msgs {
		msg, ok := raw.([]any)
		if !ok || len(msg) < 2 {
			continue
		}
		tag := strings.ToLower(strings.TrimSpace(asString(msg[0])))
		payload, _ := msg[1].(map[string]any)
		if payload == nil {
			continue
		}
		if tag == "execution_error" || tag == "prompt_outputs_failed_validation" || tag == "execution_interrupted" {
			errType := asString(payload["exception_type"])
			errMsg := asString(payload["exception_message"])
			nodeID := asString(payload["node_id"])
			nodeType := asString(payload["node_type"])
			if errMsg == "" {
				errMsg = asString(payload["message"])
			}
			parts := make([]string, 0, 4)
			if tag != "" {
				parts = append(parts, tag)
			}
			if errType != "" {
				parts = append(parts, errType)
			}
			if nodeID != "" || nodeType != "" {
				parts = append(parts, fmt.Sprintf("node=%s(%s)", nodeID, nodeType))
			}
			if errMsg != "" {
				parts = append(parts, errMsg)
			}
			if len(parts) > 0 {
				return strings.Join(parts, " | ")
			}
		}
	}
	return ""
}
