package service

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var fullVar = regexp.MustCompile(`^\{\{\s*([a-zA-Z0-9_]+)\s*\}\}$`)
var inlineVar = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_]+)\s*\}\}`)

func renderTemplate(data any, vars map[string]any) any {
	switch v := data.(type) {
	case string:
		if m := fullVar.FindStringSubmatch(v); len(m) == 2 {
			if got, ok := vars[m[1]]; ok {
				return got
			}
			return v
		}
		return inlineVar.ReplaceAllStringFunc(v, func(s string) string {
			m := inlineVar.FindStringSubmatch(s)
			if len(m) != 2 {
				return s
			}
			if got, ok := vars[m[1]]; ok {
				return fmt.Sprint(got)
			}
			return s
		})
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, vv := range v {
			out[k] = renderTemplate(vv, vars)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i := range v {
			out[i] = renderTemplate(v[i], vars)
		}
		return out
	default:
		return data
	}
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	b, _ := json.Marshal(in)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	if out == nil {
		out = map[string]any{}
	}
	return out
}

func cloneTask(t *Task) *Task {
	if t == nil {
		return nil
	}
	// Direct copy of the value fields; deep-copy only the mutable nested ones.
	// Avoids the per-progress-tick JSON marshal/unmarshal round-trip.
	out := *t
	out.Subscribers = nil // never hand out the live subscriber set
	out.Params = cloneMap(t.Params)
	if t.Outputs != nil {
		// TaskOutput is all value types, so element copy is a deep copy.
		out.Outputs = append([]TaskOutput(nil), t.Outputs...)
	}
	return &out
}

func isUIWorkflow(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	_, hasNodes := m["nodes"].([]any)
	_, hasLinks := m["links"].([]any)
	return hasNodes && hasLinks
}

func isAPIWorkflow(m map[string]any) bool {
	if len(m) == 0 {
		return false
	}
	for k, v := range m {
		if _, err := strconv.Atoi(strings.TrimSpace(k)); err != nil {
			return false
		}
		node, ok := v.(map[string]any)
		if !ok {
			return false
		}
		if _, ok := node["class_type"]; !ok {
			return false
		}
		if _, ok := node["inputs"]; !ok {
			return false
		}
	}
	return true
}

func extractAPIWorkflow(m map[string]any) (map[string]any, bool) {
	if isAPIWorkflow(m) {
		return m, true
	}
	for _, key := range []string{"prompt", "api", "workflow", "data"} {
		nested, ok := m[key].(map[string]any)
		if ok && isAPIWorkflow(nested) {
			return nested, true
		}
	}
	return nil, false
}

func applyBindings(prompt map[string]any, bindings []InputBinding, params map[string]any) error {
	for _, b := range bindings {
		nodeID := strings.TrimSpace(b.Node)
		inputName := strings.TrimSpace(b.Input)
		paramName := strings.TrimSpace(b.Param)
		if nodeID == "" || inputName == "" || paramName == "" {
			return fmt.Errorf("invalid empty workflow binding: node=%q input=%q param=%q", nodeID, inputName, paramName)
		}
		val, ok := params[paramName]
		if !ok {
			continue
		}
		node, ok := prompt[nodeID].(map[string]any)
		if !ok {
			return fmt.Errorf("workflow binding node %s is missing", nodeID)
		}
		inputs, ok := node["inputs"].(map[string]any)
		if !ok {
			return fmt.Errorf("workflow binding node %s inputs are missing", nodeID)
		}
		if _, ok := inputs[inputName]; !ok {
			return fmt.Errorf("workflow binding input %s.%s is missing", nodeID, inputName)
		}
		inputs[inputName] = val
	}
	return nil
}
