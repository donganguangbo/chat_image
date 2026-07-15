package service

import (
	"context"
	"fmt"
	"log"
	"time"
)

// pollFailFastThreshold is how many consecutive transport-level /history errors
// (connection refused / reset / dial timeout) mark the backend as unreachable so
// the poll loop bails out instead of waiting for the full task timeout. With a
// 1s ticker, connection-refused fails in ~this many seconds; a black-holed host
// (dial timeout ~10s each) takes longer but still far under the 1800s ceiling.
const pollFailFastThreshold = 20

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
	// Fail-fast on a dead backend: a burst of consecutive transport errors on
	// /history means Comfy is unreachable (crashed / restarting), not that the
	// job is still running. Bail out early instead of pinning this worker until
	// the full timeoutSec (default 1800s). Reset on any successful poll.
	consecutiveNetErrs := 0

	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("timeout waiting comfy history for prompt_id=%s (last poll error: %v)", promptID, lastErr)
		case <-ticker.C:
			history, err := comfy.GetHistory(promptID)
			if err == nil {
				consecutiveNetErrs = 0
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
				if isRetryableNetErr(err) {
					consecutiveNetErrs++
					if consecutiveNetErrs >= pollFailFastThreshold {
						log.Printf("[poll fail-fast] task=%s backend=%s prompt_id=%s: %d consecutive transport errors, backend unreachable: %v",
							taskID, comfy.BaseURL, promptID, consecutiveNetErrs, err)
						return nil, fmt.Errorf("comfy backend %s unreachable after %d consecutive poll errors: %w", comfy.BaseURL, consecutiveNetErrs, err)
					}
				} else {
					consecutiveNetErrs = 0
				}
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
