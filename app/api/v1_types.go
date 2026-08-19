package api

import (
	"fmt"
	"strconv"
	"strings"

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

type V1VideoImageReference struct {
	Image       string `json:"image"`
	ImageURL    string `json:"image_url"`
	ImageBase64 string `json:"image_base64"`
}

// V1Seconds accepts both a JSON number and a JSON string ("4" vs 4).
//
// OpenAI's Sora-shaped video API sends seconds as a *string*; our original
// clients send a number. Model gateways (new-api) forward the Sora shape
// verbatim, so rejecting the string form means the service cannot sit behind
// a gateway at all — which is exactly the intended deployment.
type V1Seconds int

func (v *V1Seconds) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*v = 0
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("seconds must be an integer (got %q)", s)
	}
	*v = V1Seconds(n)
	return nil
}

type V1VideoRequest struct {
	MotionPrompt             string                `json:"motion_prompt"`
	MotionNegativePrompt     string                `json:"motion_negative_prompt"`
	FirstFramePrompt         string                `json:"first_frame_prompt"`
	FirstFrameNegativePrompt string                `json:"first_frame_negative_prompt"`
	Size                     string                `json:"size"`
	Seconds                  V1Seconds             `json:"seconds"`
	Resolution               string                `json:"resolution"`
	Quality                  string                `json:"quality"`
	ActionID                 string                `json:"action_id"`
	SourceImage              V1VideoImageReference `json:"source_image"`
	PartnerIdentityImage     V1VideoImageReference `json:"partner_identity_image"`

	// Legacy transport fields are normalized at the API boundary. Task creation
	// and workflow rendering only receive the explicit fields above.
	Prompt         string                `json:"prompt"`
	DurationSec    int                   `json:"duration_sec"`
	Model          string                `json:"model"`
	User           string                `json:"user"`
	Params         map[string]any        `json:"params"`
	InputReference V1VideoImageReference `json:"input_reference"`
}

type V1OutputData struct {
	URL     string `json:"url,omitempty"`
	B64JSON string `json:"b64_json,omitempty"`
}

// V1ImageSyncResponse is OpenAI's standard (synchronous) images response.
// Served to bearer-authenticated callers only; see createImageTask.
type V1ImageSyncResponse struct {
	Created int64          `json:"created"`
	Data    []V1OutputData `json:"data"`
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
