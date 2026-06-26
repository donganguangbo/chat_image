package api

import (
	"chat_image/app/service"
)

const maxRequestBody = 160 << 20

type V1Handler struct {
	service *service.Server
}

func NewV1Handler(service *service.Server) *V1Handler {
	return &V1Handler{service: service}
}

type V1ImageRequest struct {
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

type V1VideoRequest struct {
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

type V1OutputData struct {
	URL     string `json:"url,omitempty"`
	B64JSON string `json:"b64_json,omitempty"`
}

type V1TaskResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Status  string         `json:"status"`
	Model   string         `json:"model,omitempty"`
	Error   string         `json:"error,omitempty"`
	Data    []V1OutputData `json:"data,omitempty"`
}

type APIErrorResponse struct {
	Error APIError `json:"error"`
}

type APIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}
