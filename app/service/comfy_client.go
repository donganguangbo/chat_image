package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type ComfyClient struct {
	BaseURL string
	HTTP    *http.Client

	// Passive health gate for failover. A transport failure (dial/connection
	// refused) marks the backend unhealthy for a short cooldown so Next() skips
	// it; after the cooldown it becomes eligible again and self-heals on the next
	// successful call (no background prober needed).
	healthMu       sync.Mutex
	unhealthyUntil time.Time
}

// comfyUnhealthyCooldown is how long a backend is skipped after a transport
// failure. Short, because a mid-cooldown routing still fails over transparently
// and re-extends the cooldown; the goal is only to prefer a known-good peer.
const comfyUnhealthyCooldown = 15 * time.Second

// Healthy reports whether the backend is currently eligible for routing.
func (c *ComfyClient) Healthy() bool {
	c.healthMu.Lock()
	defer c.healthMu.Unlock()
	return !time.Now().Before(c.unhealthyUntil)
}

// MarkUnhealthy puts the backend into cooldown after a transport failure.
func (c *ComfyClient) MarkUnhealthy() {
	c.healthMu.Lock()
	c.unhealthyUntil = time.Now().Add(comfyUnhealthyCooldown)
	c.healthMu.Unlock()
}

// MarkHealthy clears the cooldown after a successful call.
func (c *ComfyClient) MarkHealthy() {
	c.healthMu.Lock()
	c.unhealthyUntil = time.Time{}
	c.healthMu.Unlock()
}

type ComfyPool struct {
	mu      sync.Mutex
	clients []*ComfyClient
	next    int
}

func NewComfyClient(baseURL string) *ComfyClient {
	return &ComfyClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP: &http.Client{
			Timeout: 120 * time.Second,
			// Explicit dial timeout so a black-holed host fails fast instead of
			// holding a worker for the full 120s; bounded idle-conn pool so the
			// client doesn't leak sockets/goroutines over long runs.
			Transport: &http.Transport{
				DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				MaxIdleConns:        50,
				MaxIdleConnsPerHost: 8,
				IdleConnTimeout:     60 * time.Second,
			},
		},
	}
}

// isRetryableNetErr reports whether err is a CONNECTION-ESTABLISHMENT failure —
// dial timeout, connection refused, or DNS failure — meaning the request bytes
// provably never reached the server. Only such errors are safe to retry for a
// non-idempotent submit like QueuePrompt: a response timeout, mid-stream reset,
// EOF, or broken pipe is ambiguous (the server may have already received and
// enqueued the prompt) and would double-submit, so those are NOT retryable.
//
// Note: errors.As(err, &net.Error) would match almost everything here, because
// http.Client.Do wraps every error in *url.Error and *url.Error itself satisfies
// net.Error — so that check is deliberately avoided.
func isRetryableNetErr(err error) bool {
	if err == nil {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true // name resolution failed → never connected
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true // connect() refused → never connected
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		// Op=="dial" covers connect timeouts, network unreachable, and refused;
		// Op=="read"/"write" happen after the request was (at least partly) sent.
		return opErr.Op == "dial"
	}
	return false
}

// doWithRetry runs makeReq+Do up to 3 times, retrying ONLY on transport-level errors
// (see isRetryableNetErr). makeReq must rebuild the request/body on each attempt. A
// returned *http.Response (even 5xx) is handed back unretried — the caller decides,
// which keeps non-idempotent submits (QueuePrompt) from being double-queued.
func (c *ComfyClient) doWithRetry(what string, makeReq func() (*http.Request, error)) (*http.Response, error) {
	const attempts = 3
	var lastErr error
	for i := 0; i < attempts; i++ {
		req, err := makeReq()
		if err != nil {
			return nil, err
		}
		resp, err := c.HTTP.Do(req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !isRetryableNetErr(err) {
			return nil, err
		}
		if i < attempts-1 {
			backoff := time.Duration(500*(1<<i)) * time.Millisecond // 0.5s, 1s
			log.Printf("[comfy retry] %s attempt=%d backend=%s err=%v; retrying in %s", what, i+1, c.BaseURL, err, backoff)
			time.Sleep(backoff)
		}
	}
	return nil, lastErr
}

// drainClose reads (a bounded amount of) the remaining body before closing so the
// underlying connection can be reused by the idle pool instead of being discarded.
func drainClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 1<<20))
	_ = body.Close()
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
	n := len(p.clients)
	if n == 0 {
		return nil
	}
	if p.next >= n {
		p.next = 0
	}
	// Prefer a healthy backend: scan up to n starting at p.next, advancing the
	// cursor so routing stays round-robin among healthy nodes.
	for i := 0; i < n; i++ {
		c := p.clients[p.next]
		p.next = (p.next + 1) % n
		if c.Healthy() {
			return c
		}
	}
	// All backends are in cooldown — return the next one anyway rather than
	// stalling; the caller will fail over / re-mark as needed.
	c := p.clients[p.next]
	p.next = (p.next + 1) % n
	return c
}

// Size returns the number of configured backends (for bounding failover attempts).
func (p *ComfyPool) Size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.clients)
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

// Clients returns a snapshot of the backend clients (for readiness probing and
// shutdown interrupts). The slice is a copy; the *ComfyClient values are shared.
func (p *ComfyPool) Clients() []*ComfyClient {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*ComfyClient(nil), p.clients...)
}

// ClientForURL returns the pooled client whose BaseURL matches url (trailing
// slash tolerant), or nil if none. Used to target a specific backend, e.g. to
// interrupt the orphan job of a running task on shutdown.
func (p *ComfyPool) ClientForURL(u string) *ComfyClient {
	target := strings.TrimRight(strings.TrimSpace(u), "/")
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.clients {
		if c.BaseURL == target {
			return c
		}
	}
	return nil
}

// SystemStats probes ComfyUI's /system_stats endpoint as a reachability check.
// A nil return means the backend answered 2xx (ready); any error means it is
// unreachable or unhealthy. Uses a caller-supplied ctx for a short deadline.
func (c *ComfyClient) SystemStats(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/system_stats", nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer drainClose(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("comfy /system_stats status %d", resp.StatusCode)
	}
	return nil
}

// Interrupt asks ComfyUI to stop its currently-executing prompt. ComfyUI's
// /interrupt has no prompt_id argument — it cancels whatever is running — so
// this is a best-effort orphan cleanup used on graceful shutdown.
func (c *ComfyClient) Interrupt(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/interrupt", nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer drainClose(resp.Body)
	return nil
}

func (c *ComfyClient) QueuePrompt(prompt map[string]any, clientID string) (string, error) {
	body := map[string]any{"prompt": prompt, "client_id": clientID}
	buf, _ := json.Marshal(body)
	resp, err := c.doWithRetry("queue_prompt", func() (*http.Request, error) {
		req, err := http.NewRequest(http.MethodPost, c.BaseURL+"/prompt", bytes.NewReader(buf))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	})
	if err != nil {
		return "", err
	}
	defer drainClose(resp.Body)
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
	// Not wrapped in doWithRetry: the poll loop (waitForPromptOutputs) already retries.
	resp, err := c.HTTP.Get(u)
	if err != nil {
		return nil, err
	}
	defer drainClose(resp.Body)
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
	resp, err := c.doWithRetry("view", func() (*http.Request, error) {
		return http.NewRequest(http.MethodGet, u, nil)
	})
	if err != nil {
		return nil, err
	}
	defer drainClose(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("comfy /view failed: %s", strings.TrimSpace(string(raw)))
	}
	return io.ReadAll(resp.Body)
}

func (c *ComfyClient) UploadImage(localPath string, subfolder string) (string, error) {
	resp, err := c.doWithRetry("upload_image", func() (*http.Request, error) {
		fh, err := os.Open(localPath)
		if err != nil {
			return nil, err
		}
		defer fh.Close()

		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		_ = writer.WriteField("overwrite", "false")
		_ = writer.WriteField("subfolder", subfolder)
		part, err := writer.CreateFormFile("image", filepath.Base(localPath))
		if err != nil {
			return nil, err
		}
		if _, err := io.Copy(part, fh); err != nil {
			return nil, err
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
		req, err := http.NewRequest(http.MethodPost, c.BaseURL+"/upload/image", &body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", writer.FormDataContentType())
		return req, nil
	})
	if err != nil {
		return "", err
	}
	defer drainClose(resp.Body)
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
	// Track whether EVERY attempt failed at the transport level (backend
	// unreachable). If so, surface the underlying net error via %w so callers
	// (isRetryableNetErr) can fail over to another backend; if the server ever
	// responded (non-API shape), it's reachable and this is a permanent error.
	allTransport := true
	var lastNetErr error
	for _, ep := range endpoints {
		for _, payload := range payloads {
			out, status, body, err := c.postJSON(ep, payload)
			if err != nil {
				if isRetryableNetErr(err) {
					lastNetErr = err
				} else {
					allTransport = false
				}
				errs = append(errs, fmt.Sprintf("%s -> %v", ep, err))
				continue
			}
			if prompt, ok := extractAPIWorkflow(out); ok {
				return prompt, nil
			}
			allTransport = false
			keys := mapKeys(out)
			errs = append(errs, fmt.Sprintf("%s -> %d non-API shape keys=%v body=%q", ep, status, keys, truncate(body, 160)))
		}
	}
	if allTransport && lastNetErr != nil {
		return nil, fmt.Errorf("workflow convert failed on all endpoints (backend unreachable): %w", lastNetErr)
	}
	return nil, fmt.Errorf("workflow convert failed on all endpoints: %s", strings.Join(errs, "; "))
}

func (c *ComfyClient) postJSON(path string, payload any) (map[string]any, int, string, error) {
	buf, _ := json.Marshal(payload)
	resp, err := c.doWithRetry("post "+path, func() (*http.Request, error) {
		req, err := http.NewRequest(http.MethodPost, c.BaseURL+path, bytes.NewReader(buf))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	})
	if err != nil {
		return nil, 0, "", err
	}
	defer drainClose(resp.Body)

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
