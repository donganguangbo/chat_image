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
	// Drop the heavy base64 inputs from the STORED task now that we hold a private
	// working copy. Otherwise every getTask/updateTask/emit during the (up to
	// 30-min) poll re-serializes ~134MB of base64 via cloneMap(Params) — the main
	// OOM driver. Execution below uses `params`, which keeps the values it needs.
	s.stripHeavyStoredParams(taskID, task.Params)
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
