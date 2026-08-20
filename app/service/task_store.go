package service

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"strings"
	"time"
)

func (s *Server) getWorkflow(id string) (WorkflowConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cfg, ok := s.workflows[id]
	return cfg, ok
}

func (s *Server) WorkflowForCategory(category string) string {
	switch category {
	case "t2i":
		return strings.TrimSpace(s.service.DefaultWorkflows.TextToImage)
	case "edit":
		return strings.TrimSpace(s.service.DefaultWorkflows.ImageEdit)
	case "t2v":
		return strings.TrimSpace(s.service.DefaultWorkflows.TextToVideo)
	case "i2v":
		return strings.TrimSpace(s.service.DefaultWorkflows.ImageToVideo)
	default:
		return ""
	}
}

// TextBaseURL returns the text backend base URL (empty = text backend disabled).
// The OpenAI /v1/models handler uses it to decide whether to advertise the text
// model to local clients (Chatbox, new-api, etc.).
func (s *Server) TextBaseURL() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return strings.TrimSpace(s.service.Text.BaseURL)
}

// TextModelName returns the configured text backend model name (empty if unset).
func (s *Server) TextModelName() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return strings.TrimSpace(s.service.Text.Model)
}

func (s *Server) CreateTask(req CreateTaskRequest) (*Task, error) {
	return s.createTask(req)
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
	for k, v := range req.Params {
		params[k] = v
	}
	if req.Width > 0 {
		params["width"] = req.Width
	}
	if req.Height > 0 {
		params["height"] = req.Height
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
		Subscribers: map[chan string]struct{}{},
	}

	s.mu.Lock()
	s.tasks[task.ID] = task
	s.mu.Unlock()
	s.emit(task)

	// Snapshot BEFORE enqueue: once the ID is on s.queue a worker can pick it up and
	// mutate *task under the lock, so cloning task after enqueue is a real data race.
	snapshot := cloneTask(task)

	select {
	case s.queue <- task.ID:
	default:
		s.mu.Lock()
		delete(s.tasks, task.ID)
		s.mu.Unlock()
		return nil, ErrQueueFull
	}
	return snapshot, nil
}

func (s *Server) getTask(id string) *Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if t, ok := s.tasks[id]; ok {
		return cloneTask(t)
	}
	return nil
}

func (s *Server) GetTask(id string) *Task {
	return s.getTask(id)
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
	subs := make([]chan string, 0, len(t.Subscribers))
	for ch := range t.Subscribers {
		subs = append(subs, ch)
	}
	s.mu.Unlock()
	s.emitTo(snapshot, subs)
}

func (s *Server) UpdateTask(id string, fn func(*Task)) {
	s.updateTask(id, fn)
}

func (s *Server) failTask(id string, err error) {
	s.updateTask(id, func(t *Task) {
		t.Status = "failed"
		t.Progress = 100
		t.Message = "failed"
		t.Error = err.Error()
		log.Printf("[task fail] task=%s workflow=%s backend=%s prompt_id=%s err=%v",
			id, t.WorkflowID, t.Backend, t.PromptID, err)
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
		subs = make([]chan string, 0, len(t.Subscribers))
		for ch := range t.Subscribers {
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

func (s *Server) CancelTask(id string) (string, error) {
	return s.cancelTask(id)
}

func (s *Server) emit(task *Task) {
	s.mu.RLock()
	t, ok := s.tasks[task.ID]
	if !ok {
		s.mu.RUnlock()
		return
	}
	snapshot := cloneTask(t)
	subs := make([]chan string, 0, len(t.Subscribers))
	for ch := range t.Subscribers {
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
