package service

import (
	"context"
	"fmt"
	"time"
)

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
