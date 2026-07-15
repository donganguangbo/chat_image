package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

// stripHeavyStoredParams deletes large base64 input fields from the stored
// task's Params so they stop being deep-cloned on every status read/update.
// snapshotParams is a lock-free snapshot used only to decide whether a write is
// needed at all (skip the locked updateTask + emit when there's nothing heavy).
func (s *Server) stripHeavyStoredParams(taskID string, snapshotParams map[string]any) {
	hasHeavy := false
	for k := range snapshotParams {
		if strings.Contains(k, "base64") {
			hasHeavy = true
			break
		}
	}
	if !hasHeavy {
		return
	}
	s.updateTask(taskID, func(t *Task) {
		for k := range t.Params {
			if strings.Contains(k, "base64") {
				delete(t.Params, k)
			}
		}
	})
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
	cfg, ok := s.getWorkflow(task.WorkflowID)
	if !ok {
		s.failTask(taskID, fmt.Errorf("unknown workflow: %s", task.WorkflowID))
		return
	}

	// Load + parse the template once (backend-independent).
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

	// Atomic CAS: only transition queued→running (claim the task). If the task was
	// cancelled after the snapshot above, don't claim it. The backend is chosen per
	// attempt in the failover loop below, so Backend is set after a successful submit.
	taken := false
	s.updateTask(taskID, func(t *Task) {
		if t.Status != "queued" {
			return
		}
		t.Status = "running"
		t.Progress = 3
		t.Message = "loading workflow"
		taken = true
	})
	if !taken {
		return
	}

	// Keep a base64-bearing copy for (re)uploading to each backend, THEN strip the
	// heavy inputs from the stored task so the long poll window doesn't re-clone
	// them (T5). Each failover attempt re-clones baseParams (materialize mutates it).
	baseParams := cloneMap(task.Params)
	s.stripHeavyStoredParams(taskID, task.Params)

	// ── Failover: try each backend at most once for the pre-submit + submit steps.
	// Once QueuePrompt returns a prompt_id the job is committed to that backend, so
	// the poll below is fixed to it — there is no post-submit failover because
	// /prompt is non-idempotent (re-submitting could double-queue the job).
	var comfy *ComfyClient
	var rendered map[string]any
	var promptID string
	var lastErr error
	attempts := s.comfyPool.Size()
	if attempts < 1 {
		attempts = 1
	}
	for i := 0; i < attempts && promptID == ""; i++ {
		c := s.comfyPool.Next()
		if c == nil {
			lastErr = errors.New("no comfy backend available")
			break
		}
		r, pid, permanent, perr := s.prepareAndSubmit(taskID, cfg, tpl, baseParams, c)
		if perr == nil {
			c.MarkHealthy()
			comfy, rendered, promptID = c, r, pid
			break
		}
		lastErr = perr
		if permanent {
			// Not a transport failure — failing over won't help (bad workflow, or a
			// submit that may already have reached the server).
			s.failTask(taskID, perr)
			return
		}
		c.MarkUnhealthy()
		log.Printf("[failover] task=%s backend=%s unreachable, trying next backend: %v", taskID, c.BaseURL, perr)
	}
	if promptID == "" {
		if lastErr == nil {
			lastErr = errors.New("no comfy backend available")
		}
		s.failTask(taskID, fmt.Errorf("all comfy backends failed: %w", lastErr))
		return
	}

	s.updateTask(taskID, func(t *Task) {
		t.Backend = comfy.BaseURL
		t.PromptID = promptID
		t.Progress = 15
		t.Message = "submitted"
	})
	log.Printf("[task submit] task=%s workflow=%s backend=%s prompt_id=%s", taskID, task.WorkflowID, comfy.BaseURL, promptID)

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
		// Primary image-edit path failed; we silently fall back to no-swap. Log the
		// original failure so a persistently-failing primary path stays visible.
		log.Printf("[image_edit fallback] task=%s primary_err=%v", taskID, runErr)
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

// prepareAndSubmit runs the backend-specific pipeline for ONE backend: convert
// (if the template is UI-format) → materialize inputs (upload to THIS backend) →
// render → bindings → optional LoRA / inpaint detail → queue. It returns the
// rendered workflow, the prompt_id, a `permanent` flag, and an error.
//
// permanent=true means failing over to another backend will not help: either the
// workflow is malformed (backend-independent), or a submit failed in a way that
// may have reached the server — so the caller must fail the task rather than risk
// a double-queue. permanent=false means a transport failure reaching this backend
// (dial/connection refused), which is safe to retry on another backend.
func (s *Server) prepareAndSubmit(taskID string, cfg WorkflowConfig, tpl map[string]any, baseParams map[string]any, comfy *ComfyClient) (map[string]any, string, bool, error) {
	tplForRun := tpl
	if cfg.TemplateFormat == "ui" || (cfg.TemplateFormat == "auto" && isUIWorkflow(tpl)) {
		s.updateTask(taskID, func(t *Task) {
			t.Progress = 8
			t.Message = "converting UI workflow"
		})
		converted, cerr := comfy.ConvertWorkflow(tpl)
		if cerr != nil {
			return nil, "", !isRetryableNetErr(cerr), fmt.Errorf("workflow convert failed: %w", cerr)
		}
		tplForRun = converted
	}
	if !isAPIWorkflow(tplForRun) {
		return nil, "", true, fmt.Errorf("workflow is not API shape after conversion (workflow=%s)", cfg.ID)
	}

	params := cloneMap(baseParams)
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
	if _, ok := params["length_frames"]; !ok {
		durationSec := getInt(params["duration_sec"], 0)
		fps := getInt(params["fps"], 0)
		if durationSec > 0 && fps > 0 {
			params["length_frames"] = durationSec * fps
		}
	}
	for _, key := range []string{"input_image", "input_image_end"} {
		uploadTransport, merr := s.materializeImageParam(comfy, taskID, params, key)
		if merr != nil {
			// Only a comfy UploadImage transport failure is failover-worthy; a
			// download/decode failure (bad user URL, SSRF block, bad base64) is a
			// per-task error and must not poison healthy backends.
			return nil, "", !uploadTransport, fmt.Errorf("prepare %s failed: %w", key, merr)
		}
	}
	params["output_prefix"] = fmt.Sprintf("go/%s/%s_%s", cfg.ID, time.Now().Format("20060102_150405"), shortID(taskID))

	renderedAny := renderTemplate(tplForRun, params)
	rendered, ok := renderedAny.(map[string]any)
	if !ok {
		return nil, "", true, errors.New("rendered workflow is not object")
	}
	if err := applyBindings(rendered, cfg.Bindings, params); err != nil {
		return nil, "", true, fmt.Errorf("apply bindings failed: %w", err)
	}
	if cfg.Mode == "image_edit" || cfg.Mode == "t2i" || cfg.Mode == "i2v" {
		if err := applyOptionalLora(rendered, params, cfg.Mode); err != nil {
			return nil, "", true, fmt.Errorf("apply optional lora failed: %w", err)
		}
	}
	if cfg.Mode == "image_edit" {
		refineEnabled := getBool(params["refine_enabled"], false)
		if err := applyInpaintMultiDetail(rendered, params, refineEnabled); err != nil {
			return nil, "", true, fmt.Errorf("apply multi detail failed: %w", err)
		}
		applyInpaintRefineSwitch(rendered, refineEnabled)
	}

	promptID, err := comfy.QueuePrompt(rendered, taskID)
	if err != nil {
		// Only a transport failure (request never reached the server) is safe to
		// re-submit on another backend; any other error may mean the prompt was
		// accepted, so treat it as permanent to avoid a double-queue.
		return nil, "", !isRetryableNetErr(err), fmt.Errorf("queue prompt failed: %w", err)
	}
	return rendered, promptID, false, nil
}
