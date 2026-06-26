package model

import "time"

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
	Key      string `json:"key,omitempty"`
	URL      string `json:"url"`
	Size     int64  `json:"size"`
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

	Subscribers map[chan string]struct{} `json:"-"`
}

type TaskEvent struct {
	Event string `json:"event"`
	Task  *Task  `json:"task"`
}
