package service

import (
	"fmt"
	"strings"
)

type loraEntry struct {
	Name          string
	StrengthModel float64
	StrengthClip  float64
}

func parseLoraEntries(raw any) ([]loraEntry, error) {
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

		out = append(out, entry)
	}
	return out, nil
}
