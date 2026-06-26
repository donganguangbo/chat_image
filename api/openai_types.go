package api

import (
	"chat_image/service"
)

const maxRequestBody = 160 << 20

type OpenAIHandler struct {
	service *service.Server
}

func NewOpenAIHandler(service *service.Server) *OpenAIHandler {
	return &OpenAIHandler{service: service}
}

type OpenAIImageRequest struct {
	Model          string         `json:"model"`
	Prompt         string         `json:"prompt"`
	Size           string         `json:"size"`
	Quality        string         `json:"quality"`
	Style          string         `json:"style"`
	N              int            `json:"n"`
	ResponseFormat string         `json:"response_format"`
	User           string         `json:"user"`
	Params         map[string]any `json:"params"`

	InputImage       string `json:"image"`
	InputImageURL    string `json:"image_url"`
	InputImageBase64 string `json:"image_base64"`
}

type OpenAIVideoRequest struct {
	Model          string         `json:"model"`
	Prompt         string         `json:"prompt"`
	Size           string         `json:"size"`
	Seconds        int            `json:"seconds"`
	DurationSec    int            `json:"duration_sec"`
	User           string         `json:"user"`
	Params         map[string]any `json:"params"`
	InputReference struct {
		ImageURL    string `json:"image_url"`
		ImageBase64 string `json:"image_base64"`
		Image       string `json:"image"`
	} `json:"input_reference"`
}

type OpenAIOutputData struct {
	URL     string `json:"url,omitempty"`
	B64JSON string `json:"b64_json,omitempty"`
}

type OpenAITaskResponse struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Status  string             `json:"status"`
	Model   string             `json:"model,omitempty"`
	Error   string             `json:"error,omitempty"`
	Data    []OpenAIOutputData `json:"data,omitempty"`
}

type OpenAIErrorResponse struct {
	Error OpenAIError `json:"error"`
}

type OpenAIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}
