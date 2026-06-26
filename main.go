package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

var ErrQueueFull = errors.New("task queue is full")

const (
	taskRetention = time.Hour

	// maxRequestBody caps the body size in withHMACAuth (which must buffer the
	// whole body for HMAC verification).  The largest legal payload is an i2v
	// request with two base64-encoded images (input_image_base64 +
	// input_image_end_base64), each up to 50 MB decoded ≈ 67 MB base64,
	// totalling ≈ 134 MB plus JSON framing.  160 MB is a comfortable ceiling
	// while still bounding unlimited / multi-GB attack bodies.
	maxRequestBody = 160 << 20 // 160 MB
)

type WorkflowConfig struct {
	ID             string         `yaml:"id"`
	Name           string         `yaml:"name"`
	Template       string         `yaml:"template"`
	TemplateFormat string         `yaml:"template_format"`
	Mode           string         `yaml:"mode"`
	OutputKind     string         `yaml:"output_kind"`
	Defaults       map[string]any `yaml:"defaults"`
	Required       []string       `yaml:"required"`
	Bindings       []InputBinding `yaml:"bindings"`
	TimeoutSec     int            `yaml:"timeout_sec"`

	ConfigFile   string `yaml:"-"`
	TemplatePath string `yaml:"-"`
}

type InputBinding struct {
	Node  string `yaml:"node"`
	Input string `yaml:"input"`
	Param string `yaml:"param"`
}

type CategoryWorkflowConfig struct {
	TextToImage  string `yaml:"text_to_image"`
	ImageEdit    string `yaml:"image_edit"`
	TextToVideo  string `yaml:"text_to_video"`
	ImageToVideo string `yaml:"image_to_video"`
}

type ServiceConfig struct {
	ExposeWorkflowCatalog bool                   `yaml:"expose_workflow_catalog"`
	ExposeRawTaskCreate   bool                   `yaml:"expose_raw_task_create"`
	DefaultWorkflows      CategoryWorkflowConfig `yaml:"default_workflows"`
	S3                    S3FileConfig           `yaml:"s3"`
}

type CreateTaskRequest struct {
	WorkflowID     string         `json:"workflow_id"`
	Prompt         string         `json:"prompt"`
	NegativePrompt string         `json:"negative_prompt"`
	Width          int            `json:"width"`
	Height         int            `json:"height"`
	Params         map[string]any `json:"params"`
}

type TaskOutput struct {
	Kind     string `json:"kind"`
	Filename string `json:"filename"`
	// Key is the S3 object key (images|videos/{yyyymmdd}/{name}). Empty when
	// object storage is disabled or the upload failed; callers fall back to URL.
	Key  string `json:"key,omitempty"`
	URL  string `json:"url"`
	Size int64  `json:"size"`
}

type Task struct {
	ID         string         `json:"id"`
	WorkflowID string         `json:"workflow_id"`
	Backend    string         `json:"backend,omitempty"`
	Status     string         `json:"status"`
	Progress   int            `json:"progress"`
	Message    string         `json:"message"`
	Error      string         `json:"error,omitempty"`
	PromptID   string         `json:"prompt_id,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	Params     map[string]any `json:"params"`
	Outputs    []TaskOutput   `json:"outputs,omitempty"`

	subscribers map[chan string]struct{}
}

type TaskEvent struct {
	Event string `json:"event"`
	Task  *Task  `json:"task"`
}

type ComfyClient struct {
	BaseURL string
	HTTP    *http.Client
}

type ComfyPool struct {
	mu      sync.Mutex
	clients []*ComfyClient
	next    int
}

var imageEditDetailTargetModels = map[string]string{
	"face":          "bbox/face_yolov8m.pt",
	"pussy":         "bbox/pussyV2.pt",
	"nipple":        "bbox/nipple.pt",
	"breast":        "bbox/female_breast_v3.pt",
	"female_breast": "bbox/female_breast_v3.pt",
	"penis":         "bbox/penisV2.pt",
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

type Server struct {
	mu        sync.RWMutex
	workflows map[string]WorkflowConfig
	tasks     map[string]*Task
	queue     chan string

	comfyPool   *ComfyPool
	service     ServiceConfig
	workerCount int
	outputDir   string
	storage     *ObjectStorage
}

func NewServer(workflows map[string]WorkflowConfig, comfyPool *ComfyPool, service ServiceConfig, workerCount int, outputDir string, storage *ObjectStorage) *Server {
	if workerCount <= 0 {
		workerCount = 1
	}
	s := &Server{
		workflows:   workflows,
		tasks:       map[string]*Task{},
		queue:       make(chan string, 256),
		comfyPool:   comfyPool,
		service:     service,
		workerCount: workerCount,
		outputDir:   outputDir,
		storage:     storage,
	}
	for i := 0; i < workerCount; i++ {
		go s.workerLoop(i + 1)
	}
	go s.taskCleanupLoop()
	return s
}

func (s *Server) taskCleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		s.cleanupFinishedTasks()
	}
}

func (s *Server) cleanupFinishedTasks() {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, t := range s.tasks {
		switch t.Status {
		case "completed", "failed", "cancelled":
			if now.Sub(t.UpdatedAt) > taskRetention {
				delete(s.tasks, id)
			}
		}
	}
}

func (s *Server) workerLoop(_ int) {
	for id := range s.queue {
		func() {
			defer func() {
				if r := recover(); r != nil {
					s.failTask(id, fmt.Errorf("worker panic: %v", r))
				}
			}()
			s.executeTask(id)
		}()
	}
}

func (s *Server) executeTask(taskID string) {
	task := s.getTask(taskID)
	if task == nil {
		return
	}
	// If the task was cancelled while sitting in the queue, skip it. Status is
	// only mutated under s.mu, so reading the snapshot here is safe enough as a
	// fast-path; the authoritative check happens inside the updateTask call
	// below where we transition queued→running atomically.
	if task.Status != "queued" {
		return
	}
	comfy := s.comfyPool.Next()
	if comfy == nil {
		s.failTask(taskID, errors.New("no comfy backend available"))
		return
	}
	cfg, ok := s.getWorkflow(task.WorkflowID)
	if !ok {
		s.failTask(taskID, fmt.Errorf("unknown workflow: %s", task.WorkflowID))
		return
	}

	// Atomic CAS: only transition queued→running. If the task was cancelled
	// after the snapshot above (i.e. between getTask and here), don't claim it.
	taken := false
	s.updateTask(taskID, func(t *Task) {
		if t.Status != "queued" {
			return
		}
		t.Status = "running"
		t.Progress = 3
		t.Message = "loading workflow"
		t.Backend = comfy.BaseURL
		taken = true
	})
	if !taken {
		return
	}

	tplRaw, err := os.ReadFile(cfg.TemplatePath)
	if err != nil {
		s.failTask(taskID, fmt.Errorf("read template failed: %w", err))
		return
	}
	var tplAny any
	if err := json.Unmarshal(tplRaw, &tplAny); err != nil {
		s.failTask(taskID, fmt.Errorf("template JSON invalid: %w", err))
		return
	}
	tpl, ok := tplAny.(map[string]any)
	if !ok {
		s.failTask(taskID, errors.New("template root must be object"))
		return
	}

	if cfg.TemplateFormat == "ui" || (cfg.TemplateFormat == "auto" && isUIWorkflow(tpl)) {
		s.updateTask(taskID, func(t *Task) {
			t.Progress = 8
			t.Message = "converting UI workflow"
		})
		converted, cerr := comfy.ConvertWorkflow(tpl)
		if cerr != nil {
			s.failTask(taskID, fmt.Errorf("workflow convert failed: %w", cerr))
			return
		}
		tpl = converted
	}
	if !isAPIWorkflow(tpl) {
		s.failTask(taskID, fmt.Errorf("workflow is not API shape after conversion (workflow=%s)", cfg.ID))
		return
	}

	params := cloneMap(task.Params)
	if _, ok := params["size_long"]; !ok {
		w := getInt(params["width"], 0)
		h := getInt(params["height"], 0)
		if w > 0 || h > 0 {
			if w >= h {
				params["size_long"] = w
			} else {
				params["size_long"] = h
			}
		}
	}
	if cfg.Mode == "i2v" {
		// i2v head-trim workaround: the first ~Ns of i2v output tends to
		// contain morphing artifacts (e.g., anatomy growing in for coupled
		// scenes that require a second person absent from the single-frame
		// reference). We generate extra frames up front, then let the
		// workflow's ImageFromBatch node drop the first N seconds.
		// GO_GATEWAY_I2V_TRIM_HEAD_SEC accepts fractional seconds (e.g. 1.5).
		trimHeadSec := envFloat("GO_GATEWAY_I2V_TRIM_HEAD_SEC", 0)
		userDuration := getInt(params["duration_sec"], 0)
		fps := getInt(params["fps"], 0)
		if userDuration > 0 && fps > 0 {
			trimHeadFramesPreVFI := int(math.Round(trimHeadSec * float64(fps)))
			if trimHeadFramesPreVFI < 0 {
				trimHeadFramesPreVFI = 0
			}
			if trimHeadFramesPreVFI > 0 {
				// Set length_frames directly so generation includes the
				// extra frames; the generic calculator below skips when
				// length_frames is already present.
				params["length_frames"] = userDuration*fps + trimHeadFramesPreVFI
			}
			params["output_trim_head_frames"] = trimHeadFramesPreVFI * 2
			params["output_final_frames"] = userDuration * fps * 2
		}
	}
	if _, ok := params["length_frames"]; !ok {
		durationSec := getInt(params["duration_sec"], 0)
		fps := getInt(params["fps"], 0)
		if durationSec > 0 && fps > 0 {
			params["length_frames"] = durationSec * fps
		}
	}
	if _, ok := params["fps_x2"]; !ok {
		fps := getInt(params["fps"], 0)
		if fps > 0 {
			params["fps_x2"] = float64(fps * 2)
		}
	}
	for _, key := range []string{"input_image", "input_image_end"} {
		if err := s.materializeImageParam(comfy, taskID, params, key); err != nil {
			s.failTask(taskID, fmt.Errorf("prepare %s failed: %w", key, err))
			return
		}
	}
	params["output_prefix"] = fmt.Sprintf("go/%s/%s_%s", cfg.ID, time.Now().Format("20060102_150405"), shortID(taskID))

	renderedAny := renderTemplate(tpl, params)
	rendered, ok := renderedAny.(map[string]any)
	if !ok {
		s.failTask(taskID, errors.New("rendered workflow is not object"))
		return
	}
	if err := applyBindings(rendered, cfg.Bindings, params); err != nil {
		s.failTask(taskID, fmt.Errorf("apply bindings failed: %w", err))
		return
	}
	if cfg.Mode == "image_edit" || cfg.Mode == "t2i" || cfg.Mode == "i2v" {
		if err := applyOptionalLora(rendered, params, cfg.Mode); err != nil {
			s.failTask(taskID, fmt.Errorf("apply optional lora failed: %w", err))
			return
		}
	}
	if cfg.Mode == "image_edit" {
		refineEnabled := getBool(params["refine_enabled"], false)
		if err := applyInpaintMultiDetail(rendered, params, refineEnabled); err != nil {
			s.failTask(taskID, fmt.Errorf("apply multi detail failed: %w", err))
			return
		}
		applyInpaintRefineSwitch(rendered, refineEnabled)
	}

	promptID, err := comfy.QueuePrompt(rendered, taskID)
	if err != nil {
		s.failTask(taskID, err)
		return
	}

	s.updateTask(taskID, func(t *Task) {
		t.PromptID = promptID
		t.Progress = 15
		t.Message = "submitted"
	})

	timeoutSec := cfg.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 1800
	}
	imageEditAfterNodeIDs := collectImageEditAfterNodeIDs(rendered)
	outputs, runErr := s.waitForPromptOutputs(
		comfy,
		taskID,
		cfg,
		promptID,
		imageEditAfterNodeIDs,
		timeoutSec,
		15,
		90,
		"running",
	)
	if runErr != nil && cfg.Mode == "image_edit" && isMissingImageEditAfterError(runErr) {
		fallbackPrompt := cloneMap(rendered)
		applyImageEditNoSwapFallback(fallbackPrompt)
		fallbackAfterIDs := collectImageEditAfterNodeIDs(fallbackPrompt)
		fallbackPromptID, qerr := comfy.QueuePrompt(fallbackPrompt, taskID)
		if qerr != nil {
			runErr = fmt.Errorf("%v | fallback_no_swap queue failed: %v", runErr, qerr)
		} else {
			s.updateTask(taskID, func(t *Task) {
				t.PromptID = fallbackPromptID
				t.Progress = maxInt(t.Progress, 50)
				t.Message = "retry_no_swap"
			})
			fallbackOutputs, ferr := s.waitForPromptOutputs(
				comfy,
				taskID,
				cfg,
				fallbackPromptID,
				fallbackAfterIDs,
				timeoutSec,
				50,
				95,
				"retrying_no_swap",
			)
			if ferr != nil {
				runErr = fmt.Errorf("%v | fallback_no_swap failed: %v", runErr, ferr)
			} else {
				s.updateTask(taskID, func(t *Task) {
					t.Status = "completed"
					t.Progress = 100
					t.Message = "done_fallback_no_swap"
					t.Outputs = fallbackOutputs
				})
				return
			}
		}
	}
	if runErr != nil {
		s.failTask(taskID, runErr)
		return
	}
	s.updateTask(taskID, func(t *Task) {
		t.Status = "completed"
		t.Progress = 100
		t.Message = "done"
		t.Outputs = outputs
	})
}

func (s *Server) waitForPromptOutputs(
	comfy *ComfyClient,
	taskID string,
	cfg WorkflowConfig,
	promptID string,
	imageEditAfterNodeIDs []string,
	timeoutSec int,
	progressBase int,
	progressCap int,
	runningMessage string,
) ([]TaskOutput, error) {
	if timeoutSec <= 0 {
		timeoutSec = 1800
	}
	if progressCap < progressBase {
		progressCap = progressBase
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	started := time.Now()
	var lastErr error

	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("timeout waiting comfy history for prompt_id=%s (last poll error: %v)", promptID, lastErr)
		case <-ticker.C:
			history, err := comfy.GetHistory(promptID)
			if err == nil {
				if entry := findHistoryEntry(history, promptID); entry != nil {
					if historyErr := extractComfyHistoryError(entry); historyErr != "" {
						return nil, fmt.Errorf("comfy workflow failed: %s", historyErr)
					}
					outputs, derr := s.downloadOutputs(comfy, taskID, cfg, entry, imageEditAfterNodeIDs)
					if derr != nil {
						return nil, derr
					}
					return outputs, nil
				}
			} else {
				lastErr = err
			}
			elapsed := int(time.Since(started).Seconds())
			p := progressBase + elapsed
			if p > progressCap {
				p = progressCap
			}
			s.updateTask(taskID, func(t *Task) {
				if p > t.Progress {
					t.Progress = p
				}
				t.Message = runningMessage
			})
		}
	}
}

func isMissingImageEditAfterError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "no image_edit '_after' output produced")
}

func applyImageEditNoSwapFallback(prompt map[string]any) {
	if nodeSwap, ok := prompt["50"].(map[string]any); ok {
		if inputs, ok := nodeSwap["inputs"].(map[string]any); ok {
			inputs["enabled"] = false
		}
	}
	if nodeAfter, ok := prompt["8"].(map[string]any); ok {
		if inputs, ok := nodeAfter["inputs"].(map[string]any); ok {
			inputs["images"] = []any{"39", 0}
		}
	}
	delete(prompt, "80")
}

func maxInt(a, b int) int {
	if a >= b {
		return a
	}
	return b
}

func (s *Server) downloadOutputs(comfy *ComfyClient, taskID string, cfg WorkflowConfig, historyEntry map[string]any, imageEditAfterNodeIDs []string) ([]TaskOutput, error) {
	outputsAny, ok := historyEntry["outputs"].(map[string]any)
	if !ok {
		return nil, errors.New("comfy history has no outputs")
	}
	afterNodeSet := map[string]struct{}{}
	for _, id := range imageEditAfterNodeIDs {
		id = strings.TrimSpace(id)
		if id != "" {
			afterNodeSet[id] = struct{}{}
		}
	}

	imagesDir := filepath.Join(s.outputDir, "images")
	videosDir := filepath.Join(s.outputDir, "videos")
	if err := os.MkdirAll(imagesDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(videosDir, 0o755); err != nil {
		return nil, err
	}

	var out []TaskOutput
	for nodeID, nodeVal := range outputsAny {
		nodeMap, ok := nodeVal.(map[string]any)
		if !ok {
			continue
		}
		nodeID = strings.TrimSpace(nodeID)
		for key, kind := range map[string]string{"images": "image", "videos": "video", "gifs": "video"} {
			if cfg.Mode == "image_edit" && kind == "image" && len(afterNodeSet) > 0 {
				if _, ok := afterNodeSet[nodeID]; !ok {
					continue
				}
			}
			items, _ := nodeMap[key].([]any)
			for _, item := range items {
				m, ok := item.(map[string]any)
				if !ok {
					continue
				}
				filename := asString(m["filename"])
				subfolder := asString(m["subfolder"])
				fileType := asString(m["type"])
				if filename == "" {
					continue
				}
				if cfg.Mode == "image_edit" && kind == "image" && len(afterNodeSet) == 0 {
					marker := strings.ToLower(filename + " " + subfolder)
					isAfter := strings.Contains(marker, "_after") &&
						!strings.Contains(marker, "_after_swap") &&
						!strings.Contains(marker, "_before")
					if !isAfter {
						continue
					}
				}

				content, err := comfy.ViewFile(filename, subfolder, fileType)
				if err != nil {
					continue
				}

				prepared, err := prepareGeneratedOutputContent(kind, filename, content, generatedOutputConverters{
					imageToAVIF:   convertImageBytesToAVIF,
					videoToAV1MP4: convertVideoBytesToAV1MP4,
				})
				if err != nil {
					return nil, fmt.Errorf("prepare output %s: %w", filename, err)
				}
				ext := prepared.ext
				safeBase := sanitizeName(strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename)))
				if safeBase == "" {
					safeBase = "out"
				}
				safeSubfolder := sanitizeName(strings.Trim(filepath.ToSlash(subfolder), "/"))
				if safeSubfolder != "" && safeSubfolder != "." {
					safeBase = safeSubfolder + "_" + safeBase
				}
				now := time.Now()
				targetName := fmt.Sprintf("%s_%s_%s%s", shortID(taskID), now.Format("150405"), safeBase, ext)
				targetDir := imagesDir
				urlPrefix := "/outputs/images/"
				if kind == "video" {
					targetDir = videosDir
					urlPrefix = "/outputs/videos/"
				}
				targetPath := filepath.Join(targetDir, targetName)
				size, werr := writePreparedOutput(prepared, targetPath)
				if prepared.cleanup != nil {
					prepared.cleanup()
				}
				if werr != nil {
					return nil, fmt.Errorf("write output %s: %w", targetPath, werr)
				}
				item := TaskOutput{
					Kind:     kind,
					Filename: targetName,
					URL:      urlPrefix + targetName,
					Size:     size,
				}
				if s.storage != nil {
					key := gatewayObjectKey(kind, targetName, now)
					// Stream the file we just wrote to S3 instead of re-buffering it.
					if f, oerr := os.Open(targetPath); oerr != nil {
						log.Printf("[storage] open %s for upload failed: %v", targetPath, oerr)
					} else {
						uploadCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
						uerr := s.storage.UploadPrivate(uploadCtx, key, f, outputContentType(ext))
						cancel()
						f.Close()
						if uerr != nil {
							log.Printf("[storage] upload %s failed: %v", key, uerr)
						} else {
							item.Key = key
						}
					}
				}
				out = append(out, item)
			}
		}
	}

	if len(out) == 0 {
		if cfg.Mode == "image_edit" {
			if len(afterNodeSet) > 0 {
				ids := make([]string, 0, len(afterNodeSet))
				for id := range afterNodeSet {
					ids = append(ids, id)
				}
				sort.Strings(ids)
				return nil, fmt.Errorf(
					"no image_edit '_after' output produced for workflow=%s (expected node_ids=%s, expected_state=%s, available=%s)",
					cfg.ID,
					strings.Join(ids, ","),
					summarizeExpectedOutputNodes(outputsAny, ids),
					summarizeOutputNodes(outputsAny),
				)
			}
			return nil, fmt.Errorf(
				"no image_edit '_after' output produced for workflow=%s (available=%s)",
				cfg.ID,
				summarizeOutputNodes(outputsAny),
			)
		}
		return nil, fmt.Errorf("no output files produced for workflow=%s", cfg.ID)
	}
	return out, nil
}

// generatedMediaConverter transcodes content and returns the path of the
// result file plus a cleanup func (removes the temp dir). The result stays on
// disk so it can be streamed rather than re-buffered in memory.
type generatedMediaConverter func(content []byte, sourceExt string) (path string, cleanup func(), err error)

type generatedOutputConverters struct {
	imageToAVIF   generatedMediaConverter
	videoToAV1MP4 generatedMediaConverter
}

// preparedOutput is either an on-disk converted file (path + cleanup) or
// passthrough bytes (data). Exactly one of path/data is set.
type preparedOutput struct {
	ext     string
	data    []byte // passthrough (e.g. already-AVIF, or non-image/video)
	path    string // converted media on disk
	cleanup func() // non-nil iff path != ""
}

func prepareGeneratedOutputContent(kind, filename string, content []byte, converters generatedOutputConverters) (preparedOutput, error) {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		ext = ".bin"
	}
	switch kind {
	case "image":
		if ext == ".avif" {
			return preparedOutput{ext: ".avif", data: content}, nil
		}
		if converters.imageToAVIF == nil {
			return preparedOutput{}, errors.New("AVIF converter is not configured")
		}
		path, cleanup, err := converters.imageToAVIF(content, ext)
		if err != nil {
			return preparedOutput{}, fmt.Errorf("convert generated image to AVIF: %w", err)
		}
		return preparedOutput{ext: ".avif", path: path, cleanup: cleanup}, nil
	case "video":
		if converters.videoToAV1MP4 == nil {
			return preparedOutput{}, errors.New("AV1 video converter is not configured")
		}
		path, cleanup, err := converters.videoToAV1MP4(content, ext)
		if err != nil {
			return preparedOutput{}, fmt.Errorf("convert generated video to AV1 MP4: %w", err)
		}
		return preparedOutput{ext: ".mp4", path: path, cleanup: cleanup}, nil
	default:
		return preparedOutput{ext: ext, data: content}, nil
	}
}

// writePreparedOutput writes p to targetPath and returns the bytes written. For
// converted media it streams the temp file (io.Copy, not rename, so it works
// across filesystems); for passthrough it writes the in-memory bytes.
func writePreparedOutput(p preparedOutput, targetPath string) (int64, error) {
	if p.path != "" {
		src, err := os.Open(p.path)
		if err != nil {
			return 0, err
		}
		defer src.Close()
		dst, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return 0, err
		}
		n, err := io.Copy(dst, src)
		if cerr := dst.Close(); err == nil {
			err = cerr
		}
		return n, err
	}
	if err := os.WriteFile(targetPath, p.data, 0o644); err != nil {
		return 0, err
	}
	return int64(len(p.data)), nil
}

// convertImageBytesToAVIF transcodes content to AVIF and returns the path of the
// result file plus a cleanup func that removes its temp dir. On error it cleans
// up itself and returns ("", nil, err). The output is left on disk (not read
// back into memory) so the caller can stream it to its target / object storage.
func convertImageBytesToAVIF(content []byte, sourceExt string) (string, func(), error) {
	if len(content) == 0 {
		return "", nil, errors.New("empty image content")
	}

	tmpDir, err := os.MkdirTemp("", "go_gateway_avif_*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }

	inputPath := filepath.Join(tmpDir, "input"+sanitizeMediaInputExt(sourceExt))
	outputPath := filepath.Join(tmpDir, "output.avif")
	if err := os.WriteFile(inputPath, content, 0o600); err != nil {
		cleanup()
		return "", nil, err
	}

	baseArgs := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-y",
		"-i", inputPath,
		"-frames:v", "1",
		"-an",
		"-pix_fmt", "yuv420p",
	}
	attempts := []struct {
		name string
		args []string
	}{
		{"libsvtav1", append(append([]string{}, baseArgs...), "-c:v", "libsvtav1", "-crf", "30", "-preset", "8", outputPath)},
		{"libaom-av1", append(append([]string{}, baseArgs...), "-c:v", "libaom-av1", "-crf", "30", "-b:v", "0", "-cpu-used", "6", outputPath)},
		{"default-av1", append(append([]string{}, baseArgs...), "-crf", "30", outputPath)},
	}
	var failures []string
	for _, attempt := range attempts {
		_ = os.Remove(outputPath)
		output, err := exec.Command("ffmpeg", attempt.args...).CombinedOutput()
		if err != nil {
			msg := strings.TrimSpace(string(output))
			if msg == "" {
				msg = err.Error()
			}
			failures = append(failures, fmt.Sprintf("%s: %s", attempt.name, truncate(msg, 1024)))
			continue
		}

		if fi, err := os.Stat(outputPath); err != nil || fi.Size() == 0 {
			failures = append(failures, fmt.Sprintf("%s: produced empty/missing AVIF", attempt.name))
			continue
		}
		return outputPath, cleanup, nil
	}
	cleanup()
	return "", nil, fmt.Errorf("ffmpeg AVIF conversion failed: %s", strings.Join(failures, "; "))
}

// convertVideoBytesToAV1MP4 mirrors convertImageBytesToAVIF: it returns the
// output file path + cleanup func and leaves the encoded MP4 on disk rather than
// reading it back into memory.
func convertVideoBytesToAV1MP4(content []byte, sourceExt string) (string, func(), error) {
	if len(content) == 0 {
		return "", nil, errors.New("empty video content")
	}

	tmpDir, err := os.MkdirTemp("", "go_gateway_av1_video_*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }

	inputPath := filepath.Join(tmpDir, "input"+sanitizeMediaInputExt(sourceExt))
	outputPath := filepath.Join(tmpDir, "output.mp4")
	if err := os.WriteFile(inputPath, content, 0o600); err != nil {
		cleanup()
		return "", nil, err
	}

	baseArgs := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-y",
		"-i", inputPath,
		"-map", "0:v:0",
		"-an",
		"-pix_fmt", "yuv420p",
		"-movflags", "+faststart",
	}
	attempts := []struct {
		name string
		args []string
	}{
		{"libsvtav1", append(append([]string{}, baseArgs...), "-c:v", "libsvtav1", "-crf", "28", "-preset", "8", outputPath)},
		{"libaom-av1", append(append([]string{}, baseArgs...), "-c:v", "libaom-av1", "-crf", "28", "-b:v", "0", "-cpu-used", "6", outputPath)},
		{"librav1e", append(append([]string{}, baseArgs...), "-c:v", "librav1e", "-qp", "84", "-speed", "6", outputPath)},
	}
	var failures []string
	for _, attempt := range attempts {
		_ = os.Remove(outputPath)
		output, err := exec.Command("ffmpeg", attempt.args...).CombinedOutput()
		if err != nil {
			msg := strings.TrimSpace(string(output))
			if msg == "" {
				msg = err.Error()
			}
			failures = append(failures, fmt.Sprintf("%s: %s", attempt.name, truncate(msg, 1024)))
			continue
		}

		if fi, err := os.Stat(outputPath); err != nil || fi.Size() == 0 {
			failures = append(failures, fmt.Sprintf("%s: produced empty/missing MP4", attempt.name))
			continue
		}
		return outputPath, cleanup, nil
	}
	cleanup()
	return "", nil, fmt.Errorf("ffmpeg AV1 MP4 conversion failed: %s", strings.Join(failures, "; "))
}

func sanitizeMediaInputExt(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	if ext == "" || ext == ".bin" {
		return ".img"
	}
	if !strings.HasPrefix(ext, ".") || strings.ContainsAny(ext, `/\:`) {
		return ".img"
	}
	return ext
}

func outputContentType(ext string) string {
	if strings.EqualFold(strings.TrimSpace(ext), ".avif") {
		return "image/avif"
	}
	return mime.TypeByExtension(strings.ToLower(strings.TrimSpace(ext)))
}

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

func (s *Server) getWorkflow(id string) (WorkflowConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cfg, ok := s.workflows[id]
	return cfg, ok
}

func (s *Server) createTask(req CreateTaskRequest) (*Task, error) {
	cfg, ok := s.getWorkflow(req.WorkflowID)
	if !ok {
		return nil, fmt.Errorf("workflow not found: %s", req.WorkflowID)
	}

	params := cloneMap(cfg.Defaults)
	params["prompt"] = strings.TrimSpace(req.Prompt)
	if req.NegativePrompt != "" {
		params["negative_prompt"] = strings.TrimSpace(req.NegativePrompt)
	}
	if req.Width > 0 {
		params["width"] = req.Width
	}
	if req.Height > 0 {
		params["height"] = req.Height
	}
	for k, v := range req.Params {
		params[k] = v
	}
	if req.Width > 0 || req.Height > 0 {
		_, sizeLongInReq := req.Params["size_long"]
		if !sizeLongInReq {
			if req.Width >= req.Height {
				params["size_long"] = req.Width
			} else {
				params["size_long"] = req.Height
			}
		}
	}

	if seed := getInt64(params["seed"], -1); seed < 0 {
		params["seed"] = rand.Int63n(1 << 53)
	}
	if _, ok := params["end_at_step_stage1"]; !ok {
		stage1 := getInt(params["steps_stage1"], 1)
		if stage1 < 1 {
			stage1 = 1
		}
		params["end_at_step_stage1"] = stage1 - 1
	}
	if err := normalizeImageEditDetailParams(cfg.Mode, params); err != nil {
		return nil, err
	}

	for _, key := range cfg.Required {
		if !hasRequiredParam(params, key) {
			return nil, fmt.Errorf("missing required param: %s", key)
		}
	}

	now := time.Now().UTC()
	task := &Task{
		ID:          genID(),
		WorkflowID:  cfg.ID,
		Status:      "queued",
		Progress:    0,
		Message:     "queued",
		CreatedAt:   now,
		UpdatedAt:   now,
		Params:      params,
		subscribers: map[chan string]struct{}{},
	}

	s.mu.Lock()
	s.tasks[task.ID] = task
	s.mu.Unlock()
	s.emit(task)

	select {
	case s.queue <- task.ID:
	default:
		s.mu.Lock()
		delete(s.tasks, task.ID)
		s.mu.Unlock()
		return nil, ErrQueueFull
	}
	return cloneTask(task), nil
}

func (s *Server) getTask(id string) *Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if t, ok := s.tasks[id]; ok {
		return cloneTask(t)
	}
	return nil
}

func (s *Server) updateTask(id string, fn func(*Task)) {
	s.mu.Lock()
	t, ok := s.tasks[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	fn(t)
	t.UpdatedAt = time.Now().UTC()
	snapshot := cloneTask(t)
	subs := make([]chan string, 0, len(t.subscribers))
	for ch := range t.subscribers {
		subs = append(subs, ch)
	}
	s.mu.Unlock()
	s.emitTo(snapshot, subs)
}

func (s *Server) failTask(id string, err error) {
	s.updateTask(id, func(t *Task) {
		t.Status = "failed"
		t.Progress = 100
		t.Message = "failed"
		t.Error = err.Error()
	})
}

// cancelTask attempts to cancel a task. The returned status is the task's
// status AFTER the cancel attempt:
//   - "cancelled": queued task we successfully cancelled (caller should refund)
//   - "running":   task is currently using the GPU; cannot cancel (caller surfaces 409)
//   - "completed": work already finished, nothing to cancel
//   - "failed":    task already failed, nothing to cancel
//
// If the task is unknown, returns "" with an error.
func (s *Server) cancelTask(id string) (string, error) {
	var (
		status string
		subs   []chan string
		snap   *Task
	)
	s.mu.Lock()
	t, ok := s.tasks[id]
	if !ok {
		s.mu.Unlock()
		return "", fmt.Errorf("task not found")
	}
	switch t.Status {
	case "queued":
		t.Status = "cancelled"
		t.Progress = 100
		t.Message = "cancelled by user"
		t.UpdatedAt = time.Now().UTC()
		status = "cancelled"
		snap = cloneTask(t)
		subs = make([]chan string, 0, len(t.subscribers))
		for ch := range t.subscribers {
			subs = append(subs, ch)
		}
	case "running":
		status = "running"
	default:
		// completed / failed / cancelled — terminal states, return as-is
		status = t.Status
	}
	s.mu.Unlock()
	if snap != nil {
		s.emitTo(snap, subs)
	}
	return status, nil
}

func (s *Server) emit(task *Task) {
	s.mu.RLock()
	t, ok := s.tasks[task.ID]
	if !ok {
		s.mu.RUnlock()
		return
	}
	snapshot := cloneTask(t)
	subs := make([]chan string, 0, len(t.subscribers))
	for ch := range t.subscribers {
		subs = append(subs, ch)
	}
	s.mu.RUnlock()
	s.emitTo(snapshot, subs)
}

func (s *Server) emitTo(task *Task, subs []chan string) {
	evt := TaskEvent{Event: "task_update", Task: task}
	payload, _ := json.Marshal(evt)
	for _, ch := range subs {
		select {
		case ch <- string(payload):
		default:
		}
	}
}

func loadWorkflows(configDir string) (map[string]WorkflowConfig, error) {
	entries, err := os.ReadDir(configDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]WorkflowConfig{}, nil
		}
		return nil, err
	}
	out := map[string]WorkflowConfig{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.ToLower(e.Name())
		if !(strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")) {
			continue
		}
		path := filepath.Join(configDir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var cfg WorkflowConfig
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("yaml parse failed (%s): %w", e.Name(), err)
		}
		cfg.ID = strings.TrimSpace(cfg.ID)
		cfg.Name = strings.TrimSpace(cfg.Name)
		if cfg.ID == "" {
			return nil, fmt.Errorf("workflow id missing in %s", e.Name())
		}
		if cfg.Template == "" {
			return nil, fmt.Errorf("workflow template missing in %s", e.Name())
		}
		cfg.ConfigFile = path
		if filepath.IsAbs(cfg.Template) {
			cfg.TemplatePath = cfg.Template
		} else {
			cfg.TemplatePath = filepath.Clean(filepath.Join(filepath.Dir(path), cfg.Template))
		}
		cfg.TemplateFormat = strings.TrimSpace(strings.ToLower(cfg.TemplateFormat))
		if cfg.TemplateFormat == "" {
			cfg.TemplateFormat = "auto"
		}
		if cfg.TemplateFormat != "auto" && cfg.TemplateFormat != "api" && cfg.TemplateFormat != "ui" {
			return nil, fmt.Errorf("invalid template_format in %s: %s", e.Name(), cfg.TemplateFormat)
		}
		if cfg.Defaults == nil {
			cfg.Defaults = map[string]any{}
		}
		if cfg.OutputKind == "" {
			cfg.OutputKind = "image"
		}
		if _, err := os.Stat(cfg.TemplatePath); err != nil {
			return nil, fmt.Errorf("template not found for workflow=%s: %s", cfg.ID, cfg.TemplatePath)
		}
		if _, exists := out[cfg.ID]; exists {
			return nil, fmt.Errorf("duplicate workflow id: %s", cfg.ID)
		}
		out[cfg.ID] = cfg
	}
	if len(out) == 0 {
		return out, nil
	}
	return out, nil
}

func defaultServiceConfig() ServiceConfig {
	return ServiceConfig{
		ExposeWorkflowCatalog: false,
		ExposeRawTaskCreate:   false,
		DefaultWorkflows:      CategoryWorkflowConfig{},
	}
}

func loadServiceConfig(configPath string, workflows map[string]WorkflowConfig) (ServiceConfig, error) {
	cfg := defaultServiceConfig()
	raw, err := os.ReadFile(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return cfg, err
	}

	var fileCfg ServiceConfig
	if err := yaml.Unmarshal(raw, &fileCfg); err != nil {
		return cfg, fmt.Errorf("service yaml parse failed: %w", err)
	}

	if strings.TrimSpace(fileCfg.DefaultWorkflows.TextToImage) != "" {
		cfg.DefaultWorkflows.TextToImage = strings.TrimSpace(fileCfg.DefaultWorkflows.TextToImage)
	}
	if strings.TrimSpace(fileCfg.DefaultWorkflows.ImageEdit) != "" {
		cfg.DefaultWorkflows.ImageEdit = strings.TrimSpace(fileCfg.DefaultWorkflows.ImageEdit)
	}
	if strings.TrimSpace(fileCfg.DefaultWorkflows.TextToVideo) != "" {
		cfg.DefaultWorkflows.TextToVideo = strings.TrimSpace(fileCfg.DefaultWorkflows.TextToVideo)
	}
	if strings.TrimSpace(fileCfg.DefaultWorkflows.ImageToVideo) != "" {
		cfg.DefaultWorkflows.ImageToVideo = strings.TrimSpace(fileCfg.DefaultWorkflows.ImageToVideo)
	}
	cfg.ExposeWorkflowCatalog = fileCfg.ExposeWorkflowCatalog
	cfg.ExposeRawTaskCreate = fileCfg.ExposeRawTaskCreate
	cfg.S3 = fileCfg.S3

	if len(workflows) == 0 {
		return cfg, nil
	}

	check := func(category, workflowID string) error {
		if workflowID == "" {
			return fmt.Errorf("service config missing workflow for category %s", category)
		}
		if _, ok := workflows[workflowID]; !ok {
			return fmt.Errorf("service config category %s points to unknown workflow_id=%s", category, workflowID)
		}
		return nil
	}
	if err := check("text_to_image", cfg.DefaultWorkflows.TextToImage); err != nil {
		return cfg, err
	}
	if err := check("image_edit", cfg.DefaultWorkflows.ImageEdit); err != nil {
		return cfg, err
	}
	if err := check("text_to_video", cfg.DefaultWorkflows.TextToVideo); err != nil {
		return cfg, err
	}
	if err := check("image_to_video", cfg.DefaultWorkflows.ImageToVideo); err != nil {
		return cfg, err
	}
	return cfg, nil
}

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
	out.subscribers = nil // never hand out the live subscriber set
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
			continue
		}
		val, ok := params[paramName]
		if !ok {
			continue
		}
		node, ok := prompt[nodeID].(map[string]any)
		if !ok {
			continue
		}
		inputs, ok := node["inputs"].(map[string]any)
		if !ok {
			continue
		}
		inputs[inputName] = val
	}
	return nil
}

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

func applyOptionalLora(prompt map[string]any, params map[string]any, mode string) error {
	if prompt == nil || params == nil {
		return nil
	}

	// New multi-LoRA path: params.loras array takes precedence over legacy single-slot fields.
	if rawLoras, ok := params["loras"]; ok && rawLoras != nil {
		loras, err := parseLoraEntries(rawLoras, mode)
		if err != nil {
			return err
		}
		if len(loras) == 0 {
			return nil
		}
		if _, hasOld := params["lora_name"]; hasOld {
			log.Printf("applyOptionalLora: both params.loras and params.lora_name present; loras[] wins, lora_name ignored")
		}
		if getBool(params["lora_enabled"], false) {
			log.Printf("applyOptionalLora: both params.loras and params.lora_enabled=true present; loras[] wins, lora_enabled ignored")
		}
		if mode == "i2v" {
			return applyMultiLoraI2V(prompt, params, loras)
		}
		// image_edit / t2i: legacy single-slot semantics — use the first entry of loras[].
		return applyLegacyLoraFromMultiEntry(prompt, params, mode, loras[0])
	}

	// No `loras` field. For i2v, never fall back to legacy single-slot behavior — it was
	// never supported pre-change, and the spec says legacy lora_name/lora_enabled are not
	// read on the new i2v path.
	if mode == "i2v" {
		return nil
	}

	// Legacy single-slot path — preserved bit-for-bit for image_edit / t2i backward compatibility.
	return applyLegacyLora(prompt, params, mode)
}

func applyLegacyLora(prompt map[string]any, params map[string]any, mode string) error {
	enabled := getBool(params["lora_enabled"], false)
	if !enabled {
		return nil
	}

	loraName := strings.TrimSpace(asString(params["lora_name"]))
	if loraName == "" {
		return nil
	}
	loraName = strings.TrimPrefix(loraName, "models/loras/")
	loraName = strings.TrimPrefix(loraName, "loras/")

	baseNodeID := asString(params["lora_base_node_id"])
	if baseNodeID == "" {
		baseNodeID = "69"
	}
	baseNode, ok := prompt[baseNodeID].(map[string]any)
	if !ok {
		return fmt.Errorf("lora base node not found: %s", baseNodeID)
	}
	baseClassType := asString(baseNode["class_type"])
	if baseClassType == "" {
		return fmt.Errorf("lora base node class_type missing: %s", baseNodeID)
	}

	modelOnly := getBool(params["lora_model_only"], false)
	strengthModel := getFloat64(params["lora_strength_model"], 0.8)
	strengthClip := getFloat64(params["lora_strength_clip"], strengthModel)

	originalNodeIDs := make([]string, 0, len(prompt))
	for id := range prompt {
		originalNodeIDs = append(originalNodeIDs, id)
	}

	newNodeID := strconv.Itoa(maxNumericNodeID(prompt) + 1)
	if modelOnly {
		prompt[newNodeID] = map[string]any{
			"inputs": map[string]any{
				"model":          []any{baseNodeID, 0},
				"lora_name":      loraName,
				"strength_model": strengthModel,
			},
			"class_type": "LoraLoaderModelOnly",
			"_meta": map[string]any{
				"title": "LoraLoaderModelOnly",
			},
		}
	} else {
		prompt[newNodeID] = map[string]any{
			"inputs": map[string]any{
				"model":          []any{baseNodeID, 0},
				"clip":           []any{baseNodeID, 1},
				"lora_name":      loraName,
				"strength_model": strengthModel,
				"strength_clip":  strengthClip,
			},
			"class_type": "LoraLoader",
			"_meta": map[string]any{
				"title": "LoraLoader",
			},
		}
	}

	scope := strings.ToLower(strings.TrimSpace(asString(params["lora_scope"])))
	if scope == "" && strings.TrimSpace(mode) == "image_edit" {
		// For image_edit flows, default to applying LoRA on the initial generation chain
		// and keep face-detail/refine branch on base model to avoid pose drift.
		scope = "before_only"
	}
	if scope == "before_only" && strings.TrimSpace(mode) == "image_edit" {
		excludeClass := map[string]struct{}{
			"FaceDetailer": {},
		}
		relinkNodeInputSourceForNodeIDsExcludingClass(prompt, originalNodeIDs, baseNodeID, 0, newNodeID, 0, excludeClass)
		if !modelOnly {
			relinkNodeInputSourceForNodeIDsExcludingClass(prompt, originalNodeIDs, baseNodeID, 1, newNodeID, 1, excludeClass)
		}
	} else {
		relinkNodeInputSourceForNodeIDsExcludingClass(prompt, originalNodeIDs, baseNodeID, 0, newNodeID, 0, nil)
		if !modelOnly {
			relinkNodeInputSourceForNodeIDsExcludingClass(prompt, originalNodeIDs, baseNodeID, 1, newNodeID, 1, nil)
		}
	}
	return nil
}

// applyLegacyLoraFromMultiEntry lets image_edit / t2i callers send params.loras[] too,
// by mapping the first entry onto the existing single-slot fields and reusing the
// legacy code path (so FaceDetailer scope, CLIP wiring, etc. stay identical).
func applyLegacyLoraFromMultiEntry(prompt map[string]any, params map[string]any, mode string, entry loraEntry) error {
	shim := cloneMap(params)
	shim["lora_enabled"] = true
	shim["lora_name"] = entry.Name
	shim["lora_strength_model"] = entry.StrengthModel
	shim["lora_strength_clip"] = entry.StrengthClip
	// Preserve existing lora_base_node_id / lora_scope / lora_model_only if caller set them.
	return applyLegacyLora(prompt, shim, mode)
}

type loraEntry struct {
	Name          string
	Attach        string // "stage1" | "stage2" | "both" (i2v); empty for image_edit/t2i
	StrengthModel float64
	StrengthClip  float64
}

func parseLoraEntries(raw any, mode string) ([]loraEntry, error) {
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

		if mode == "i2v" {
			attach := strings.ToLower(strings.TrimSpace(asString(m["attach"])))
			switch attach {
			case "stage1", "stage2", "both":
				// ok
			case "":
				return nil, fmt.Errorf("params.loras[%d].attach is required for i2v mode (stage1|stage2|both)", i)
			default:
				return nil, fmt.Errorf("params.loras[%d].attach=%q invalid, allowed: stage1, stage2, both", i, attach)
			}
			entry.Attach = attach
		}

		out = append(out, entry)
	}
	return out, nil
}

func applyMultiLoraI2V(prompt map[string]any, params map[string]any, loras []loraEntry) error {
	stageNodeIDs, err := readLoraStageNodeIDs(params)
	if err != nil {
		return err
	}

	// Validate stage node IDs up front: any stage referenced by a LoRA entry must resolve
	// to a node that exists in the rendered workflow.
	needed := map[string]bool{}
	for _, e := range loras {
		switch e.Attach {
		case "stage1", "stage2":
			needed[e.Attach] = true
		case "both":
			needed["stage1"] = true
			needed["stage2"] = true
		}
	}
	for stage := range needed {
		baseNodeID := stageNodeIDs[stage]
		if baseNodeID == "" {
			return fmt.Errorf("i2v LoRA attach=%s requires workflow config lora_stage_node_ids.%s", stage, stage)
		}
		if _, ok := prompt[baseNodeID].(map[string]any); !ok {
			return fmt.Errorf("i2v LoRA %s base node %q not found in workflow", stage, baseNodeID)
		}
	}

	// Group LoRA entries by their *resolved* base node ID rather than by stage name, so
	// AIO workflows (where stage1 and stage2 alias to the same UNET) produce a single
	// chain instead of two competing ones.
	type stageGroup struct {
		baseNodeID string
		entries    []loraEntry
	}
	groupIndex := map[string]int{}
	groups := []stageGroup{}
	addEntry := func(stage string, entry loraEntry) {
		baseNodeID := stageNodeIDs[stage]
		if idx, ok := groupIndex[baseNodeID]; ok {
			groups[idx].entries = append(groups[idx].entries, entry)
			return
		}
		groupIndex[baseNodeID] = len(groups)
		groups = append(groups, stageGroup{baseNodeID: baseNodeID, entries: []loraEntry{entry}})
	}
	for _, e := range loras {
		switch e.Attach {
		case "stage1", "stage2":
			addEntry(e.Attach, e)
		case "both":
			addEntry("stage1", e)
			if stageNodeIDs["stage2"] != stageNodeIDs["stage1"] {
				addEntry("stage2", e)
			}
		}
	}

	// Snapshot the original node IDs once so later relinks target only pre-existing consumers,
	// not the LoRA nodes we're about to insert.
	originalNodeIDs := make([]string, 0, len(prompt))
	for id := range prompt {
		originalNodeIDs = append(originalNodeIDs, id)
	}

	for _, g := range groups {
		currentSource := g.baseNodeID
		for _, entry := range g.entries {
			newNodeID := strconv.Itoa(maxNumericNodeID(prompt) + 1)
			prompt[newNodeID] = map[string]any{
				"inputs": map[string]any{
					"model":          []any{currentSource, 0},
					"lora_name":      entry.Name,
					"strength_model": entry.StrengthModel,
				},
				"class_type": "LoraLoaderModelOnly",
				"_meta": map[string]any{
					"title": "LoraLoaderModelOnly",
				},
			}
			currentSource = newNodeID
		}
		if currentSource != g.baseNodeID {
			relinkNodeInputSourceForNodeIDsExcludingClass(prompt, originalNodeIDs, g.baseNodeID, 0, currentSource, 0, nil)
		}
	}
	return nil
}

func readLoraStageNodeIDs(params map[string]any) (map[string]string, error) {
	raw, ok := params["lora_stage_node_ids"]
	if !ok || raw == nil {
		return nil, fmt.Errorf("i2v LoRA request requires workflow config lora_stage_node_ids (e.g. {stage1: \"279\", stage2: \"280\"})")
	}
	out := map[string]string{}
	switch m := raw.(type) {
	case map[string]any:
		for k, v := range m {
			out[strings.ToLower(strings.TrimSpace(k))] = asString(v)
		}
	case map[any]any:
		for k, v := range m {
			out[strings.ToLower(strings.TrimSpace(asString(k)))] = asString(v)
		}
	default:
		return nil, fmt.Errorf("lora_stage_node_ids must be a map, got %T", raw)
	}
	return out, nil
}

func normalizeImageEditDetailParams(mode string, params map[string]any) error {
	if strings.TrimSpace(mode) != "image_edit" || params == nil {
		return nil
	}

	models := make([]string, 0, 6)
	seen := map[string]struct{}{}
	addModel := func(model string) {
		m := normalizeBBoxModelPath(model)
		if m == "" {
			return
		}
		if _, ok := seen[m]; ok {
			return
		}
		seen[m] = struct{}{}
		models = append(models, m)
	}
	addTarget := func(target string) error {
		t := strings.ToLower(strings.TrimSpace(target))
		if t == "" {
			return nil
		}
		model, ok := imageEditDetailTargetModels[t]
		if !ok {
			return fmt.Errorf("invalid params.detail_target=%q, allowed: %s", t, strings.Join(allowedDetailTargets(), ", "))
		}
		addModel(model)
		return nil
	}

	for _, target := range asStringSlice(params["detail_targets"]) {
		if err := addTarget(target); err != nil {
			return err
		}
	}
	if target := asString(params["detail_target"]); target != "" {
		if err := addTarget(target); err != nil {
			return err
		}
	}

	for _, model := range asStringSlice(params["detail_bbox_models"]) {
		addModel(model)
	}
	if model := asString(params["detail_bbox_model"]); model != "" {
		addModel(model)
	}

	if len(models) == 0 {
		return nil
	}
	params["detail_bbox_model"] = models[0]
	modelsAny := make([]any, 0, len(models))
	for _, model := range models {
		modelsAny = append(modelsAny, model)
	}
	params["detail_bbox_models"] = modelsAny
	return nil
}

func allowedDetailTargets() []string {
	out := make([]string, 0, len(imageEditDetailTargetModels))
	for k := range imageEditDetailTargetModels {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func asStringSlice(v any) []string {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		return splitCSVLike(x)
	case []string:
		out := make([]string, 0, len(x))
		for _, item := range x {
			s := strings.TrimSpace(item)
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			s := asString(item)
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		s := asString(v)
		if s == "" {
			return nil
		}
		return splitCSVLike(s)
	}
}

func splitCSVLike(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t'
	})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		s := strings.TrimSpace(part)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func normalizeBBoxModelPath(model string) string {
	s := strings.TrimSpace(model)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "/") {
		return "bbox/" + s
	}
	return s
}

func applyInpaintMultiDetail(prompt map[string]any, params map[string]any, refineEnabled bool) error {
	if !refineEnabled || prompt == nil {
		return nil
	}

	models := make([]string, 0, 6)
	seen := map[string]struct{}{}
	for _, model := range asStringSlice(params["detail_bbox_models"]) {
		m := normalizeBBoxModelPath(model)
		if m == "" {
			continue
		}
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		models = append(models, m)
	}
	if len(models) == 0 {
		if single := normalizeBBoxModelPath(asString(params["detail_bbox_model"])); single != "" {
			models = append(models, single)
		}
	}
	if len(models) <= 1 {
		return nil
	}

	baseDetector, ok := prompt["61"].(map[string]any)
	if !ok {
		return errors.New("workflow missing node 61 (UltralyticsDetectorProvider)")
	}
	baseDetectorInputs, ok := baseDetector["inputs"].(map[string]any)
	if !ok {
		return errors.New("workflow node 61 inputs invalid")
	}
	baseDetectorInputs["model_name"] = models[0]

	baseDetailer, ok := prompt["60"].(map[string]any)
	if !ok {
		return errors.New("workflow missing node 60 (FaceDetailer)")
	}
	if _, ok := baseDetailer["inputs"].(map[string]any); !ok {
		return errors.New("workflow node 60 inputs invalid")
	}

	originalNodeIDs := make([]string, 0, len(prompt))
	for id := range prompt {
		originalNodeIDs = append(originalNodeIDs, id)
	}

	nextID := maxNumericNodeID(prompt) + 1
	previousDetailerID := "60"
	for _, model := range models[1:] {
		detectorClone, err := deepCopyNodeMap(baseDetector)
		if err != nil {
			return fmt.Errorf("clone detector failed: %w", err)
		}
		detectorInputs, _ := detectorClone["inputs"].(map[string]any)
		detectorInputs["model_name"] = model
		detectorID := strconv.Itoa(nextID)
		nextID++
		prompt[detectorID] = detectorClone

		detailerClone, err := deepCopyNodeMap(baseDetailer)
		if err != nil {
			return fmt.Errorf("clone detailer failed: %w", err)
		}
		detailerInputs, _ := detailerClone["inputs"].(map[string]any)
		detailerInputs["bbox_detector"] = []any{detectorID, 0}
		detailerInputs["image"] = []any{previousDetailerID, 0}
		detailerID := strconv.Itoa(nextID)
		nextID++
		prompt[detailerID] = detailerClone

		previousDetailerID = detailerID
	}

	relinkNodeInputSourceForNodeIDsExcludingClass(prompt, originalNodeIDs, "60", 0, previousDetailerID, 0, nil)
	return nil
}

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

func applyInpaintRefineSwitch(prompt map[string]any, refineEnabled bool) {
	// Keep default chain when refine is enabled.
	if refineEnabled {
		return
	}
	// Match Python gateway behavior:
	// - route final SaveImage to ReActor output (node 50)
	// - drop extra after_swap save node (node 80)
	if nodeAfter, ok := prompt["8"].(map[string]any); ok {
		if inputs, ok := nodeAfter["inputs"].(map[string]any); ok {
			inputs["images"] = []any{"50", 0}
		}
	}
	delete(prompt, "80")
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

// isBlockedIP returns true for loopback, private, link-local, and unspecified
// addresses that must not be reachable from user-supplied URLs.
func isBlockedIP(ip net.IP) bool {
	return ip == nil ||
		ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified()
}

// safeHTTPClient is a package-level hardened client used for all outbound
// image downloads. It blocks SSRF targets (cloud metadata, internal services)
// by inspecting the already-resolved IP in DialContext's Control hook, and
// re-validates redirect destinations.
var safeHTTPClient = &http.Client{
	Timeout: 60 * time.Second,
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
			Control: func(network, address string, _ syscall.RawConn) error {
				host, _, err := net.SplitHostPort(address)
				if err != nil {
					return fmt.Errorf("ssrf-guard: cannot parse address %q: %w", address, err)
				}
				ip := net.ParseIP(host)
				if isBlockedIP(ip) {
					return fmt.Errorf("ssrf-guard: address %q is not allowed", address)
				}
				return nil
			},
		}).DialContext,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("ssrf-guard: too many redirects")
		}
		host := req.URL.Hostname()
		if ip := net.ParseIP(host); ip != nil {
			if isBlockedIP(ip) {
				return fmt.Errorf("ssrf-guard: redirect to %q is not allowed", host)
			}
		}
		return nil
	},
}

func (s *Server) materializeImageParam(comfy *ComfyClient, taskID string, params map[string]any, key string) error {
	raw := asString(params[key])
	rawURL := asString(params[key+"_url"])
	rawBase64 := asString(params[key+"_base64"])

	var localPath string

	switch {
	case rawURL != "":
		p, err := downloadImageFromURL(rawURL)
		if err != nil {
			return err
		}
		localPath = p
		defer func() { _ = os.Remove(p) }()
	case rawBase64 != "":
		p, err := decodeBase64ImageToTemp(rawBase64)
		if err != nil {
			return err
		}
		localPath = p
		defer func() { _ = os.Remove(p) }()
	case raw != "":
		// Do not touch the local filesystem; keep original value for
		// Comfy-side input resolution (Comfy resolves against its own input dir).
		return nil
	default:
		return nil
	}

	s.updateTask(taskID, func(t *Task) {
		if t.Progress < 10 {
			t.Progress = 10
		}
		t.Message = "uploading input image"
	})
	uploaded, err := comfy.UploadImage(localPath, "go_gateway_uploads")
	if err != nil {
		return err
	}
	params[key] = uploaded
	return nil
}

func downloadImageFromURL(rawURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", fmt.Errorf("invalid image url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("image url must be http or https")
	}

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "chat-image-go-gateway/1.0")

	// Use the package-level hardened client (SSRF protection, redirect limits).
	resp, err := safeHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("download image failed: %s %s", resp.Status, strings.TrimSpace(string(raw)))
	}

	const maxSize = int64(50 << 20)
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return "", err
	}
	if int64(len(payload)) > maxSize {
		return "", fmt.Errorf("remote image too large: %d bytes", len(payload))
	}
	if len(payload) == 0 {
		return "", errors.New("remote image is empty")
	}

	ext := inferImageExt(resp.Header.Get("Content-Type"), u.Path, payload)
	tmp, err := os.CreateTemp("", "go_gateway_img_*"+ext)
	if err != nil {
		return "", err
	}
	defer tmp.Close()
	if _, err := tmp.Write(payload); err != nil {
		// Fix 3: clean up orphan temp file on write failure.
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}

func decodeBase64ImageToTemp(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("empty base64 image")
	}

	contentType := ""
	if strings.HasPrefix(strings.ToLower(s), "data:") {
		comma := strings.Index(s, ",")
		if comma <= 0 {
			return "", errors.New("invalid data url image")
		}
		head := s[:comma]
		if !strings.Contains(strings.ToLower(head), ";base64") {
			return "", errors.New("data url is not base64")
		}
		parts := strings.SplitN(head, ";", 2)
		contentType = strings.TrimPrefix(parts[0], "data:")
		s = s[comma+1:]
	}

	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		stripped := strings.TrimRight(s, "=")
		var err2 error
		decoded, err2 = base64.RawStdEncoding.DecodeString(stripped)
		if err2 != nil {
			decoded, err2 = base64.RawURLEncoding.DecodeString(stripped)
			if err2 != nil {
				return "", fmt.Errorf("decode base64 image failed: %w", err)
			}
		}
	}
	if len(decoded) == 0 {
		return "", errors.New("base64 image is empty")
	}
	if len(decoded) > 50<<20 {
		return "", fmt.Errorf("base64 image too large: %d bytes", len(decoded))
	}

	ext := inferImageExt(contentType, "", decoded)
	tmp, err := os.CreateTemp("", "go_gateway_img_b64_*"+ext)
	if err != nil {
		return "", err
	}
	defer tmp.Close()
	if _, err := tmp.Write(decoded); err != nil {
		// Fix 3: clean up orphan temp file on write failure.
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}

func inferImageExt(contentType, path string, payload []byte) string {
	if mediaType, _, err := mime.ParseMediaType(contentType); err == nil {
		switch strings.ToLower(mediaType) {
		case "image/png":
			return ".png"
		case "image/jpeg", "image/jpg":
			return ".jpg"
		case "image/webp":
			return ".webp"
		case "image/gif":
			return ".gif"
		case "image/bmp":
			return ".bmp"
		case "image/avif":
			return ".avif"
		}
	}

	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp", ".avif":
		return ext
	}

	switch strings.ToLower(http.DetectContentType(payload)) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "image/bmp":
		return ".bmp"
	}
	return ".png"
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Gateway-Timestamp, X-Gateway-Nonce, X-Gateway-Signature")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// HMAC request authentication
//
// Every API request must carry three headers:
//   X-Gateway-Timestamp: Unix seconds (string)
//   X-Gateway-Nonce:     hex-encoded random bytes, single use within the window
//   X-Gateway-Signature: hex(HMAC-SHA256(secret, METHOD\n PATH\n TS\n NONCE\n BODY))
//
// The secret never travels on the wire. Even if the channel is observed, an
// attacker cannot forge a new request (no secret) and cannot replay an old one
// (nonce single-use, timestamp ±300s window).
//
// /healthz and /outputs/ bypass auth — health is for monitors; outputs are
// static files served to end-users whose browsers cannot attach headers.
// ─────────────────────────────────────────────────────────────────────────────

const (
	hmacTimestampHeader = "X-Gateway-Timestamp"
	hmacNonceHeader     = "X-Gateway-Nonce"
	hmacSignatureHeader = "X-Gateway-Signature"
	hmacTimeWindowSec   = 300
)

type nonceCache struct {
	mu      sync.Mutex
	entries map[string]int64 // nonce -> unix seconds seen
}

func newNonceCache() *nonceCache {
	nc := &nonceCache{entries: make(map[string]int64)}
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			nc.cleanup()
		}
	}()
	return nc
}

// claim returns true when the nonce was not previously seen within the window
// and is now registered; false means the nonce was already used (replay).
func (nc *nonceCache) claim(nonce string, now int64) bool {
	nc.mu.Lock()
	defer nc.mu.Unlock()
	if seen, ok := nc.entries[nonce]; ok && now-seen <= hmacTimeWindowSec {
		return false
	}
	nc.entries[nonce] = now
	return true
}

func (nc *nonceCache) cleanup() {
	now := time.Now().Unix()
	nc.mu.Lock()
	defer nc.mu.Unlock()
	for k, t := range nc.entries {
		if now-t > hmacTimeWindowSec {
			delete(nc.entries, k)
		}
	}
}

func bypassAuth(r *http.Request) bool {
	// CORS preflight has no body and never carries auth headers in this product;
	// the real request that follows is signed.
	if r.Method == http.MethodOptions {
		return true
	}
	// Health probes (k8s, uptime monitors) need no auth.
	if r.URL.Path == "/healthz" {
		return true
	}
	// NOTE: /outputs/* used to bypass; it no longer does. chat-web fronts all
	// output access via /api/media/* and adds the HMAC signature server-side,
	// so end users never hit the gateway directly. See chat-web's
	// src/app/api/media/[...path]/route.ts.
	return false
}

func withHMACAuth(next http.Handler, secret string, nc *nonceCache) http.Handler {
	secretBytes := []byte(secret)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bypassAuth(r) {
			next.ServeHTTP(w, r)
			return
		}

		tsStr := r.Header.Get(hmacTimestampHeader)
		nonce := r.Header.Get(hmacNonceHeader)
		sigHex := r.Header.Get(hmacSignatureHeader)
		if tsStr == "" || nonce == "" || sigHex == "" {
			http.Error(w, "unauthorized: missing signature headers", http.StatusUnauthorized)
			return
		}

		ts, err := strconv.ParseInt(tsStr, 10, 64)
		if err != nil {
			http.Error(w, "unauthorized: invalid timestamp", http.StatusUnauthorized)
			return
		}
		now := time.Now().Unix()
		if ts < now-hmacTimeWindowSec || ts > now+hmacTimeWindowSec {
			http.Error(w, "unauthorized: timestamp out of window", http.StatusUnauthorized)
			return
		}

		if !nc.claim(nonce, now) {
			http.Error(w, "unauthorized: nonce reused", http.StatusUnauthorized)
			return
		}

		// Read body once; preserve it for the downstream handler.
		// Wrap with MaxBytesReader before reading to prevent OOM on oversized
		// bodies; HMAC requires the full body in memory.
		var body []byte
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
			var readErr error
			body, readErr = io.ReadAll(r.Body)
			if readErr != nil {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}

		path := r.URL.Path
		if r.URL.RawQuery != "" {
			path = path + "?" + r.URL.RawQuery
		}
		msg := r.Method + "\n" + path + "\n" + tsStr + "\n" + nonce + "\n" + string(body)
		mac := hmac.New(sha256.New, secretBytes)
		mac.Write([]byte(msg))
		expected := mac.Sum(nil)

		got, err := hex.DecodeString(sigHex)
		if err != nil || !hmac.Equal(got, expected) {
			http.Error(w, "unauthorized: invalid signature", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// migrateOutputsToS3 uploads every file under {outputDir}/images and
// {outputDir}/videos to the bucket, keyed exactly like new gateway outputs
// (images|videos/{yyyymmdd(file mtime, UTC)}/{name}) via gatewayObjectKey, so the
// gateway sweep and the DB-ref backfill never produce divergent keys. It reads
// the DB nothing and never writes to disk, so it is safe to run alongside a live
// gateway. dry=true logs the keys without uploading.
func migrateOutputsToS3(ctx context.Context, storage *ObjectStorage, outputDir string, dry bool) error {
	dirs := []struct{ sub, kind string }{{"images", "image"}, {"videos", "video"}}
	var total, uploaded, skipped, failed int
	for _, d := range dirs {
		dir := filepath.Join(outputDir, d.sub)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				log.Printf("[migrate] %s not present, skipping", dir)
				continue
			}
			return fmt.Errorf("read dir %s: %w", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			info, err := e.Info()
			if err != nil {
				log.Printf("[migrate] stat %s/%s failed: %v", d.sub, e.Name(), err)
				failed++
				continue
			}
			name := e.Name()
			key := gatewayObjectKey(d.kind, name, info.ModTime())
			total++
			if dry {
				log.Printf("[migrate:dry] %s/%s -> %s", d.sub, name, key)
				continue
			}
			ok, err := storage.Exists(ctx, key)
			if err != nil {
				log.Printf("[migrate] head %s failed: %v", key, err)
				failed++
				continue
			}
			if ok {
				skipped++
				continue
			}
			content, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				log.Printf("[migrate] read %s/%s failed: %v", d.sub, name, err)
				failed++
				continue
			}
			ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
			if err := storage.UploadPrivate(ctx, key, bytes.NewReader(content), ct); err != nil {
				log.Printf("[migrate] upload %s failed: %v", key, err)
				failed++
				continue
			}
			uploaded++
			if uploaded%50 == 0 {
				log.Printf("[migrate] progress: uploaded=%d skipped=%d failed=%d", uploaded, skipped, failed)
			}
		}
	}
	suffix := ""
	if dry {
		suffix = " (dry-run; nothing uploaded)"
	}
	log.Printf("[migrate] done. total=%d uploaded=%d skipped(existing)=%d failed=%d%s",
		total, uploaded, skipped, failed, suffix)
	if failed > 0 {
		return fmt.Errorf("%d file(s) failed to migrate", failed)
	}
	return nil
}

func main() {
	rand.Seed(time.Now().UnixNano())

	configDir := env("WORKFLOW_CONFIG_DIR", "./configs/workflows")
	serviceConfigFile := env("SERVICE_CONFIG_FILE", "./configs/service.yaml")
	comfyURLs := parseComfyURLs()
	listenAddr := env("LISTEN_ADDR", ":8090")
	outputDir := env("OUTPUT_DIR", "./outputs")

	// HMAC shared secret is required. Without it every request is rejected as
	// unauthorized — no silent fall-through to an unprotected gateway. Generate one
	// with `openssl rand -hex 32` and share it between gateway and chat-web.
	gatewaySecret := strings.TrimSpace(env("GATEWAY_SECRET", ""))
	if gatewaySecret == "" {
		log.Fatal("GATEWAY_SECRET is required (set a strong random string, e.g. `openssl rand -hex 32`)")
	}

	absConfigDir, _ := filepath.Abs(configDir)
	absServiceConfigFile, _ := filepath.Abs(serviceConfigFile)
	absOutputDir, _ := filepath.Abs(outputDir)
	if err := os.MkdirAll(filepath.Join(absOutputDir, "images"), 0o755); err != nil {
		log.Fatalf("mkdir outputs/images failed: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(absOutputDir, "videos"), 0o755); err != nil {
		log.Fatalf("mkdir outputs/videos failed: %v", err)
	}

	workflows, err := loadWorkflows(absConfigDir)
	if err != nil {
		log.Fatalf("load workflows failed: %v", err)
	}
	log.Printf("loaded workflows: %d", len(workflows))
	for _, cfg := range workflows {
		log.Printf("- %s -> %s", cfg.ID, cfg.TemplatePath)
	}
	serviceCfg, err := loadServiceConfig(absServiceConfigFile, workflows)
	if err != nil {
		log.Fatalf("load service config failed: %v", err)
	}
	log.Printf(
		"category defaults: t2i=%s edit=%s t2v=%s i2v=%s",
		serviceCfg.DefaultWorkflows.TextToImage,
		serviceCfg.DefaultWorkflows.ImageEdit,
		serviceCfg.DefaultWorkflows.TextToVideo,
		serviceCfg.DefaultWorkflows.ImageToVideo,
	)
	log.Printf("expose internal: workflows=%v raw_task_create=%v", serviceCfg.ExposeWorkflowCatalog, serviceCfg.ExposeRawTaskCreate)

	comfyPool := NewComfyPool(comfyURLs)
	if len(comfyPool.URLs()) == 0 {
		log.Fatal("no comfy backends configured (set COMFY_URL or COMFY_URLS)")
	}
	workerCount := envInt("GO_WORKERS", len(comfyPool.URLs()))
	// S3 credentials come from the environment (S3_ACCESS_KEY / S3_SECRET_KEY),
	// never from service.yaml, so secrets stay out of the repo. Endpoint/region/
	// bucket live in service.yaml but env vars may override them too.
	s3cfg := serviceCfg.S3
	if v := env("S3_ENDPOINT", ""); v != "" {
		s3cfg.Endpoint = v
	}
	if v := env("S3_REGION", ""); v != "" {
		s3cfg.Region = v
	}
	if v := env("S3_BUCKET", ""); v != "" {
		s3cfg.Bucket = v
	}
	if v := env("S3_ACCESS_KEY", ""); v != "" {
		s3cfg.AccessKey = v
	}
	if v := env("S3_SECRET_KEY", ""); v != "" {
		s3cfg.SecretKey = v
	}
	storage, s3Enabled, err := NewObjectStorageFromConfig(s3cfg)
	if err != nil {
		log.Fatalf("object storage misconfigured: %v", err)
	}
	if s3Enabled {
		log.Printf("[storage] Linode object storage enabled (bucket=%s)", serviceCfg.S3.Bucket)
	} else {
		log.Printf("[storage] object storage disabled (s3.bucket unset in service.yaml); writing to local disk only")
	}

	// One-shot outputs migration: MIGRATE_OUTPUTS=dry lists keys, =1 uploads, then
	// the process exits without serving. Safe to run beside a live gateway — no DB,
	// no disk writes, no port bind. Keys mirror the chat-side backfill, so a later
	// DB-ref backfill finds the objects already present and only rewrites refs.
	if mode := strings.TrimSpace(env("MIGRATE_OUTPUTS", "")); mode != "" {
		if !s3Enabled || storage == nil {
			log.Fatal("MIGRATE_OUTPUTS set but object storage is disabled; configure s3 in service.yaml")
		}
		dry := strings.EqualFold(mode, "dry")
		log.Printf("[migrate] starting outputs migration (dir=%s dry=%v)", absOutputDir, dry)
		if err := migrateOutputsToS3(context.Background(), storage, absOutputDir, dry); err != nil {
			log.Fatalf("[migrate] %v", err)
		}
		return
	}

	srv := NewServer(workflows, comfyPool, serviceCfg, workerCount, absOutputDir, storage)

	mux := http.NewServeMux()
	registerRoutes(mux, srv, absOutputDir)

	log.Printf("go gateway listening on %s", listenAddr)
	log.Printf("comfy backends: %s", strings.Join(comfyPool.URLs(), ", "))
	log.Printf("go workers: %d", workerCount)
	log.Printf("config dir: %s", absConfigDir)
	log.Printf("service config: %s", absServiceConfigFile)
	log.Printf("output dir: %s", absOutputDir)
	log.Printf("hmac auth: enabled (window=%ds, /healthz and /outputs bypass)", hmacTimeWindowSec)
	nonces := newNonceCache()
	handler := withHMACAuth(withCORS(mux), gatewaySecret, nonces)
	httpSrv := &http.Server{
		Addr:              listenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := httpSrv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func parseComfyURLs() []string {
	primary := strings.TrimSpace(os.Getenv("COMFY_URL"))
	all := strings.TrimSpace(os.Getenv("COMFY_URLS"))
	if all == "" {
		if primary == "" {
			primary = "http://127.0.0.1:8188"
		}
		return []string{primary}
	}
	parts := strings.FieldsFunc(all, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	out := make([]string, 0, len(parts)+1)
	seen := map[string]struct{}{}
	if primary != "" {
		normalized := strings.TrimRight(primary, "/")
		out = append(out, normalized)
		seen[normalized] = struct{}{}
	}
	for _, p := range parts {
		u := strings.TrimRight(strings.TrimSpace(p), "/")
		if u == "" {
			continue
		}
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	return out
}

func env(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

func envInt(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func envFloat(key string, fallback float64) float64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		return fallback
	}
	return f
}
