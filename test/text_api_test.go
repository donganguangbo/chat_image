package test

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"chat_image/app/api"
	"chat_image/app/service"
)

func performTextRequest(t *testing.T, s *service.Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	api.Register(mux, s, t.TempDir(), service.StorageModeLocal)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestV1ChatCompletionsProxiesToTextBackend(t *testing.T) {
	var gotPath string
	var gotPayload map[string]any
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Fatalf("decode backend request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-test",
			"object":  "chat.completion",
			"model":   gotPayload["model"],
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "你好"}}},
		})
	}))
	defer backend.Close()

	s := service.NewServer(
		map[string]service.WorkflowConfig{},
		nil,
		service.ServiceConfig{Text: service.TextConfig{BaseURL: backend.URL, Model: "qwen-local"}},
		1,
		"",
		nil,
	)
	rec := performTextRequest(t, s, `{"messages":[{"role":"user","content":"你好"}]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("backend path = %q", gotPath)
	}
	if gotPayload["model"] != "qwen-local" {
		t.Fatalf("backend model = %v, want qwen-local", gotPayload["model"])
	}
	var resp struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Model != "qwen-local" || resp.Choices[0].Message.Content != "你好" {
		t.Fatalf("response = %+v", resp)
	}
}

func TestV1ChatCompletionsRequiresTextBackend(t *testing.T) {
	s := service.NewServer(map[string]service.WorkflowConfig{}, nil, service.ServiceConfig{}, 1, "", nil)
	rec := performTextRequest(t, s, `{"messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
}

func TestV1ChatCompletionsStreamsBackendEvents(t *testing.T) {
	releaseBackend := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: one\n\n"))
		w.(http.Flusher).Flush()
		<-releaseBackend
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer backend.Close()
	defer close(releaseBackend)

	s := service.NewServer(
		map[string]service.WorkflowConfig{},
		nil,
		service.ServiceConfig{Text: service.TextConfig{BaseURL: backend.URL, Model: "qwen-local"}},
		1,
		"",
		nil,
	)
	mux := http.NewServeMux()
	api.Register(mux, s, t.TempDir(), service.StorageModeLocal)
	gateway := httptest.NewServer(mux)
	defer gateway.Close()

	req, err := http.NewRequest(http.MethodPost, gateway.URL+"/v1/chat/completions", strings.NewReader(`{"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	lineCh := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(resp.Body).ReadString('\n')
		lineCh <- line
	}()

	select {
	case line := <-lineCh:
		if strings.TrimSpace(line) != "data: one" {
			t.Fatalf("first stream line = %q", line)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first stream event")
	}
}
