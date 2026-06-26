package service

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"time"
)

func getInt64(v any, fallback int64) int64 {
	switch x := v.(type) {
	case int:
		return int64(x)
	case int32:
		return int64(x)
	case int64:
		return x
	case float64:
		return int64(x)
	case json.Number:
		n, err := x.Int64()
		if err == nil {
			return n
		}
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		if err == nil {
			return n
		}
	}
	return fallback
}

func getInt(v any, fallback int) int {
	return int(getInt64(v, int64(fallback)))
}

func getFloat64(v any, fallback float64) float64 {
	switch x := v.(type) {
	case float32:
		return float64(x)
	case float64:
		return x
	case int:
		return float64(x)
	case int32:
		return float64(x)
	case int64:
		return float64(x)
	case json.Number:
		if f, err := x.Float64(); err == nil {
			return f
		}
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil {
			return f
		}
	}
	return fallback
}

func getBool(v any, fallback bool) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		s := strings.TrimSpace(strings.ToLower(x))
		switch s {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	case int:
		return x != 0
	case int32:
		return x != 0
	case int64:
		return x != 0
	case float32:
		return x != 0
	case float64:
		return x != 0
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return n != 0
		}
	}
	return fallback
}
func isEmptyValue(v any) bool {
	if v == nil {
		return true
	}
	s := strings.TrimSpace(fmt.Sprint(v))
	return s == ""
}

func hasRequiredParam(params map[string]any, key string) bool {
	if !isEmptyValue(params[key]) {
		return true
	}
	switch key {
	case "input_image":
		return !isEmptyValue(params["input_image_url"]) || !isEmptyValue(params["input_image_base64"])
	case "input_image_end":
		return !isEmptyValue(params["input_image_end_url"]) || !isEmptyValue(params["input_image_end_base64"])
	default:
		return false
	}
}

func asString(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	}
	s := strings.TrimSpace(fmt.Sprint(v))
	if s == "<nil>" {
		return ""
	}
	return s
}

func mapKeys(m map[string]any) []string {
	if m == nil {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 8 {
		return keys[:8]
	}
	return keys
}

func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func sanitizeName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\\", "_")
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, ":", "_")
	s = strings.ReplaceAll(s, " ", "_")
	return s
}
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func genID() string {
	return fmt.Sprintf("%x%x", time.Now().UnixNano(), rand.Uint64())
}
