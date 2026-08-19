package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"chat_image/app/service"
)

func (h *V1Handler) HandleImageGeneration(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req V1ImageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	h.createImageTask(w, r, "t2i", "image.generation", req)
}

func (h *V1Handler) HandleImageEdit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	req, err := parseImageEditRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.createImageTask(w, r, "edit", "image.edit", req)
}

func (h *V1Handler) createImageTask(w http.ResponseWriter, r *http.Request, category, object string, req V1ImageRequest) {
	workflowID := h.service.WorkflowForCategory(category)
	if workflowID == "" {
		writeAPIError(w, http.StatusBadRequest, "category workflow not configured")
		return
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		writeAPIError(w, http.StatusBadRequest, "prompt is required")
		return
	}
	width, height, err := parseSize(req.Size)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	params := cloneMap(req.Params)
	copyCommonParams(params, req.Model, req.User)
	if req.Quality != "" {
		params["quality"] = req.Quality
	}
	if req.Style != "" {
		params["style"] = req.Style
	}
	if req.N > 0 {
		params["n"] = req.N
	}
	if req.ResponseFormat != "" {
		params["response_format"] = req.ResponseFormat
	}
	if req.InputImage != "" {
		params["input_image"] = strings.TrimSpace(req.InputImage)
	}
	if req.InputImageURL != "" {
		params["input_image_url"] = strings.TrimSpace(req.InputImageURL)
	}
	if req.InputImageBase64 != "" {
		params["input_image_base64"] = strings.TrimSpace(req.InputImageBase64)
	}

	task, err := h.service.CreateTask(service.CreateTaskRequest{
		WorkflowID: workflowID,
		Prompt:     prompt,
		Width:      width,
		Height:     height,
		Params:     params,
	})
	if err != nil {
		writeTaskError(w, err)
		return
	}
	// Two response contracts, told apart by how the caller authenticated
	// (see service.AuthedViaBearer):
	//   - bearer (model gateways, e.g. new-api): OpenAI's images API is
	//     *synchronous* — dall-e-3 itself blocks 10-20s. Gateways bill and
	//     relay that shape; an async task envelope is useless to them since
	//     they cannot poll our private task route.
	//   - HMAC (legacy chat-web): keeps the async task envelope untouched.
	// Collapse to sync-only once the legacy caller is retired.
	if service.AuthedViaBearer(r) {
		h.respondImageSync(w, r, task.ID)
		return
	}
	writeJSON(w, http.StatusOK, v1ResponseFromTask(task, object))
}

// imageSyncWait bounds the synchronous wait. Generation measured ~15s on the
// current KREA2 workflow; 120s leaves headroom for queue depth while staying
// inside every caller's timeout (ichat client 180s > gateway relay > this).
const imageSyncWait = 120 * time.Second

// imageSyncPollInterval GetTask is an in-memory snapshot behind a mutex —
// polling it is cheap; 500ms keeps sync latency close to actual finish time.
const imageSyncPollInterval = 500 * time.Millisecond

// respondImageSync blocks until the task settles, then answers in OpenAI's
// standard images shape: {created, data:[{url}]}.
func (h *V1Handler) respondImageSync(w http.ResponseWriter, r *http.Request, taskID string) {
	deadline := time.Now().Add(imageSyncWait)
	for {
		task := h.service.GetTask(taskID)
		if task == nil {
			writeAPIError(w, http.StatusInternalServerError, "task vanished")
			return
		}
		switch task.Status {
		case "completed":
			resp := V1ImageSyncResponse{Created: task.CreatedAt.Unix()}
			for _, out := range task.Outputs {
				if out.URL == "" || out.Kind != "image" {
					continue
				}
				resp.Data = append(resp.Data, V1OutputData{URL: out.URL})
			}
			if len(resp.Data) == 0 {
				// A completed task with no image output is a workflow bug,
				// not a caller error — surface it as such.
				writeAPIError(w, http.StatusInternalServerError, "task completed without image output")
				return
			}
			writeJSON(w, http.StatusOK, resp)
			return
		case "failed":
			writeAPIError(w, http.StatusInternalServerError, task.Error)
			return
		case "cancelled":
			writeAPIError(w, http.StatusConflict, "task cancelled")
			return
		}
		if time.Now().After(deadline) {
			// 504, not 500: the task may still finish; the caller chose a
			// synchronous contract and ran out of patience, nothing is broken.
			writeAPIError(w, http.StatusGatewayTimeout, "image generation timed out after "+imageSyncWait.String())
			return
		}
		select {
		case <-r.Context().Done():
			// Caller hung up — stop burning the poll loop. The task itself
			// keeps running; its output lands in storage like any other.
			return
		case <-time.After(imageSyncPollInterval):
		}
	}
}

func (h *V1Handler) HandleVideoCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// Unknown fields are tolerated on purpose: model gateways (new-api) attach
	// their own fields when forwarding. Strict rejection means the service
	// cannot sit behind a gateway. Typos in optional fields now fall through
	// silently — the explicit/legacy contract check below still catches mixed
	// payloads, which is the mistake that actually bit us.
	var req V1VideoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req, err := normalizeVideoRequest(req)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	motionPrompt := strings.TrimSpace(req.MotionPrompt)
	if motionPrompt == "" {
		writeAPIError(w, http.StatusBadRequest, "motion_prompt is required")
		return
	}
	motionNegativePrompt := strings.TrimSpace(req.MotionNegativePrompt)
	if motionNegativePrompt == "" {
		writeAPIError(w, http.StatusBadRequest, "motion_negative_prompt is required")
		return
	}
	width, height, err := parseSize(req.Size)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	params := map[string]any{
		"motion_prompt":          motionPrompt,
		"motion_negative_prompt": motionNegativePrompt,
	}
	actionID, err := normalizeVideoActionID(req.ActionID)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if actionID != "" {
		params["action_id"] = actionID
	}
	if req.Seconds > 0 {
		params["duration_sec"] = int(req.Seconds)
	}
	if resolution := strings.TrimSpace(req.Resolution); resolution != "" {
		params["resolution"] = resolution
	}
	if quality := strings.TrimSpace(req.Quality); quality != "" {
		params["quality"] = quality
	}
	category := "t2v"
	hasSourceImage, err := copyVideoImageReference(params, "source_image", req.SourceImage)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if hasSourceImage {
		category = "i2v"
		firstFramePrompt := strings.TrimSpace(req.FirstFramePrompt)
		firstFrameNegativePrompt := strings.TrimSpace(req.FirstFrameNegativePrompt)
		if firstFramePrompt == "" {
			writeAPIError(w, http.StatusBadRequest, "first_frame_prompt is required for image-to-video")
			return
		}
		if firstFrameNegativePrompt == "" {
			writeAPIError(w, http.StatusBadRequest, "first_frame_negative_prompt is required for image-to-video")
			return
		}
		params["first_frame_prompt"] = firstFramePrompt
		params["first_frame_negative_prompt"] = firstFrameNegativePrompt
	} else if strings.TrimSpace(req.FirstFramePrompt) != "" || strings.TrimSpace(req.FirstFrameNegativePrompt) != "" {
		writeAPIError(w, http.StatusBadRequest, "source_image is required when first-frame prompts are provided")
		return
	}
	hasPartnerIdentity, err := copyVideoImageReference(params, "partner_identity_image", req.PartnerIdentityImage)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if hasPartnerIdentity && !hasSourceImage {
		writeAPIError(w, http.StatusBadRequest, "source_image is required with partner_identity_image")
		return
	}

	workflowID := h.service.WorkflowForCategory(category)
	if workflowID == "" {
		writeAPIError(w, http.StatusBadRequest, "category workflow not configured")
		return
	}
	task, err := h.service.CreateTask(service.CreateTaskRequest{
		WorkflowID: workflowID,
		Prompt:     motionPrompt,
		Width:      width,
		Height:     height,
		Params:     params,
	})
	if err != nil {
		writeTaskError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v1ResponseFromTask(task, "video"))
}

func (h *V1Handler) HandleVideoByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/videos/")
	if path == "" {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(path, "/")
	id := parts[0]
	if len(parts) == 2 && parts[1] == "content" {
		h.handleVideoContent(w, r, id)
		return
	}
	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		task := h.service.GetTask(id)
		if task == nil {
			writeAPIError(w, http.StatusNotFound, "video not found")
			return
		}
		writeJSON(w, http.StatusOK, v1ResponseFromTask(task, "video"))
	case http.MethodDelete:
		status, err := h.service.CancelTask(id)
		if err != nil {
			writeAPIError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "object": "video.deleted", "deleted": status == "cancelled"})
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *V1Handler) handleVideoContent(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	task := h.service.GetTask(id)
	if task == nil {
		writeAPIError(w, http.StatusNotFound, "video not found")
		return
	}
	if task.Status != "completed" {
		writeAPIError(w, http.StatusConflict, "video is not completed")
		return
	}
	for _, out := range task.Outputs {
		if out.Kind == "video" && out.URL != "" {
			http.Redirect(w, r, out.URL, http.StatusTemporaryRedirect)
			return
		}
	}
	writeAPIError(w, http.StatusNotFound, "video content not found")
}
