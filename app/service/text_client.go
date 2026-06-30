package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type TextBackendError struct {
	Status int
	Body   string
}

var ErrTextBackendNotConfigured = errors.New("text backend is not configured")

func (e TextBackendError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("text backend failed: status=%d", e.Status)
	}
	return fmt.Sprintf("text backend failed: status=%d body=%s", e.Status, e.Body)
}

func (s *Server) TextCompletionResponse(ctx context.Context, payload map[string]any) (*http.Response, error) {
	cfg := s.service.Text
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		return nil, ErrTextBackendNotConfigured
	}
	if _, ok := payload["model"]; !ok {
		if model := strings.TrimSpace(cfg.Model); model != "" {
			payload["model"] = model
		}
	}
	if strings.TrimSpace(asString(payload["model"])) == "" {
		return nil, errors.New("model is required")
	}
	if _, ok := payload["messages"].([]any); !ok {
		return nil, errors.New("messages is required")
	}

	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey := strings.TrimSpace(cfg.APIKey); apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxRequestBody))
		if err != nil {
			return nil, err
		}
		return nil, TextBackendError{Status: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	return resp, nil
}

func (s *Server) CreateTextCompletion(ctx context.Context, payload map[string]any) ([]byte, error) {
	resp, err := s.TextCompletionResponse(ctx, payload)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRequestBody))
	if err != nil {
		return nil, err
	}
	return body, nil
}
