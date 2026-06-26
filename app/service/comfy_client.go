package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type ComfyClient struct {
	BaseURL string
	HTTP    *http.Client
}

type ComfyPool struct {
	mu      sync.Mutex
	clients []*ComfyClient
	next    int
}

func NewComfyClient(baseURL string) *ComfyClient {
	return &ComfyClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{Timeout: 120 * time.Second},
	}
}

func NewComfyPool(urls []string) *ComfyPool {
	clients := make([]*ComfyClient, 0, len(urls))
	for _, u := range urls {
		trimmed := strings.TrimRight(strings.TrimSpace(u), "/")
		if trimmed == "" {
			continue
		}
		clients = append(clients, NewComfyClient(trimmed))
	}
	return &ComfyPool{clients: clients}
}

func (p *ComfyPool) Next() *ComfyClient {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.clients) == 0 {
		return nil
	}
	if p.next >= len(p.clients) {
		p.next = 0
	}
	c := p.clients[p.next]
	p.next = (p.next + 1) % len(p.clients)
	return c
}

func (p *ComfyPool) URLs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.clients))
	for _, c := range p.clients {
		out = append(out, c.BaseURL)
	}
	return out
}

func (c *ComfyClient) QueuePrompt(prompt map[string]any, clientID string) (string, error) {
	body := map[string]any{"prompt": prompt, "client_id": clientID}
	buf, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, c.BaseURL+"/prompt", bytes.NewReader(buf))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("comfy /prompt failed: %s", strings.TrimSpace(string(raw)))
	}
	var out struct {
		PromptID string `json:"prompt_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.PromptID == "" {
		return "", errors.New("empty prompt_id from comfy")
	}
	return out.PromptID, nil
}

func (c *ComfyClient) GetHistory(promptID string) (map[string]any, error) {
	u := c.BaseURL + "/history/" + url.PathEscape(promptID)
	resp, err := c.HTTP.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("comfy /history failed: %s", strings.TrimSpace(string(raw)))
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *ComfyClient) ViewFile(filename, subfolder, fileType string) ([]byte, error) {
	q := url.Values{}
	q.Set("filename", filename)
	q.Set("subfolder", subfolder)
	q.Set("type", fileType)
	u := c.BaseURL + "/view?" + q.Encode()
	resp, err := c.HTTP.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("comfy /view failed: %s", strings.TrimSpace(string(raw)))
	}
	return io.ReadAll(resp.Body)
}

func (c *ComfyClient) UploadImage(localPath string, subfolder string) (string, error) {
	fh, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer fh.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("overwrite", "false")
	_ = writer.WriteField("subfolder", subfolder)
	part, err := writer.CreateFormFile("image", filepath.Base(localPath))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, fh); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequest(http.MethodPost, c.BaseURL+"/upload/image", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("comfy /upload/image failed: %s", strings.TrimSpace(string(raw)))
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	name := asString(out["name"])
	if name == "" {
		name = asString(out["filename"])
	}
	if name == "" {
		return "", fmt.Errorf("upload response missing filename: %s", strings.TrimSpace(string(raw)))
	}
	returnedSubfolder := asString(out["subfolder"])
	if returnedSubfolder == "" {
		return name, nil
	}
	return filepath.ToSlash(filepath.Join(returnedSubfolder, name)), nil
}

func (c *ComfyClient) ConvertWorkflow(ui map[string]any) (map[string]any, error) {
	endpoints := []string{"/workflow/convert", "/cpe/workflow/convert", "/api/cpe/workflow/convert"}
	payloads := []map[string]any{
		{"workflow": ui},
		{"prompt": ui},
		{"graph": ui},
		{"data": ui},
		ui,
	}

	var errs []string
	for _, ep := range endpoints {
		for _, payload := range payloads {
			out, status, body, err := c.postJSON(ep, payload)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s -> %v", ep, err))
				continue
			}
			if prompt, ok := extractAPIWorkflow(out); ok {
				return prompt, nil
			}
			keys := mapKeys(out)
			errs = append(errs, fmt.Sprintf("%s -> %d non-API shape keys=%v body=%q", ep, status, keys, truncate(body, 160)))
		}
	}
	return nil, fmt.Errorf("workflow convert failed on all endpoints: %s", strings.Join(errs, "; "))
}

func (c *ComfyClient) postJSON(path string, payload any) (map[string]any, int, string, error) {
	buf, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, c.BaseURL+path, bytes.NewReader(buf))
	if err != nil {
		return nil, 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	defer resp.Body.Close()

	rawBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	raw := strings.TrimSpace(string(rawBytes))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if raw == "" {
			raw = http.StatusText(resp.StatusCode)
		}
		return nil, resp.StatusCode, raw, fmt.Errorf("%s: %s", resp.Status, raw)
	}
	if raw == "" {
		return map[string]any{}, resp.StatusCode, "", nil
	}

	var out map[string]any
	if err := json.Unmarshal(rawBytes, &out); err != nil {
		return nil, resp.StatusCode, raw, err
	}
	return out, resp.StatusCode, raw, nil
}
