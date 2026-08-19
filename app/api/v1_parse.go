package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

const (
	legacyVideoMotionNegative     = "camera movement, shot change, slow motion, frozen motion, extra person, temporal discontinuity"
	legacyVideoFirstFrameNegative = "bad anatomy, fused bodies, extra limbs, duplicate person, malformed hands"
)

var videoActionIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

func parseImageEditRequest(r *http.Request) (V1ImageRequest, error) {
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		if err := r.ParseMultipartForm(maxRequestBody); err != nil {
			return V1ImageRequest{}, fmt.Errorf("invalid multipart body")
		}
		if r.MultipartForm != nil {
			defer func() { _ = r.MultipartForm.RemoveAll() }()
		}
		req := V1ImageRequest{
			Model:          r.FormValue("model"),
			Prompt:         r.FormValue("prompt"),
			Size:           r.FormValue("size"),
			Quality:        r.FormValue("quality"),
			ResponseFormat: r.FormValue("response_format"),
			User:           r.FormValue("user"),
			Params:         map[string]any{},
		}
		if imageURL := strings.TrimSpace(r.FormValue("image_url")); imageURL != "" {
			req.InputImageURL = imageURL
		}
		if r.MultipartForm != nil {
			if files := r.MultipartForm.File["image"]; len(files) > 0 {
				f, err := files[0].Open()
				if err != nil {
					return V1ImageRequest{}, fmt.Errorf("read image file: %w", err)
				}
				defer f.Close()
				b, err := io.ReadAll(f)
				if err != nil {
					return V1ImageRequest{}, fmt.Errorf("read image file: %w", err)
				}
				req.InputImageBase64 = base64.StdEncoding.EncodeToString(b)
			}
			if files := r.MultipartForm.File["mask"]; len(files) > 0 {
				f, err := files[0].Open()
				if err != nil {
					return V1ImageRequest{}, fmt.Errorf("read mask file: %w", err)
				}
				defer f.Close()
				b, err := io.ReadAll(f)
				if err != nil {
					return V1ImageRequest{}, fmt.Errorf("read mask file: %w", err)
				}
				req.Params["mask_base64"] = base64.StdEncoding.EncodeToString(b)
			}
		}
		return req, nil
	}

	var req V1ImageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return V1ImageRequest{}, fmt.Errorf("invalid JSON body")
	}
	return req, nil
}

func parseSize(size string) (int, int, error) {
	size = strings.TrimSpace(size)
	if size == "" || size == "auto" {
		return 0, 0, nil
	}
	parts := strings.Split(size, "x")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid size %q", size)
	}
	w, err := strconv.Atoi(parts[0])
	if err != nil || w <= 0 {
		return 0, 0, fmt.Errorf("invalid size %q", size)
	}
	h, err := strconv.Atoi(parts[1])
	if err != nil || h <= 0 {
		return 0, 0, fmt.Errorf("invalid size %q", size)
	}
	return w, h, nil
}

func copyCommonParams(params map[string]any, model, user string) {
	if model = strings.TrimSpace(model); model != "" {
		params["model"] = model
	}
	if user = strings.TrimSpace(user); user != "" {
		params["user"] = user
	}
}

func videoImageReferencePresent(ref V1VideoImageReference) bool {
	return strings.TrimSpace(ref.Image) != "" ||
		strings.TrimSpace(ref.ImageURL) != "" ||
		strings.TrimSpace(ref.ImageBase64) != ""
}

func videoParamString(params map[string]any, key string) string {
	value, _ := params[key].(string)
	return strings.TrimSpace(value)
}

func legacyVideoImageReference(params map[string]any, key string) V1VideoImageReference {
	return V1VideoImageReference{
		Image:       videoParamString(params, key),
		ImageURL:    videoParamString(params, key+"_url"),
		ImageBase64: videoParamString(params, key+"_base64"),
	}
}

func normalizeVideoRequest(req V1VideoRequest) (V1VideoRequest, error) {
	usesExplicitContract := strings.TrimSpace(req.MotionPrompt) != "" ||
		strings.TrimSpace(req.MotionNegativePrompt) != "" ||
		strings.TrimSpace(req.FirstFramePrompt) != "" ||
		strings.TrimSpace(req.FirstFrameNegativePrompt) != "" ||
		videoImageReferencePresent(req.SourceImage) ||
		videoImageReferencePresent(req.PartnerIdentityImage)
	usesLegacyContract := strings.TrimSpace(req.Prompt) != "" ||
		req.DurationSec != 0 ||
		strings.TrimSpace(req.Model) != "" ||
		strings.TrimSpace(req.User) != "" ||
		len(req.Params) != 0 ||
		videoImageReferencePresent(req.InputReference)

	if usesExplicitContract && usesLegacyContract {
		return V1VideoRequest{}, fmt.Errorf("video request must not mix legacy and explicit fields")
	}
	if !usesLegacyContract {
		return req, nil
	}

	legacyNegativePrompt := videoParamString(req.Params, "negative_prompt")
	motionNegativePrompt := legacyNegativePrompt
	if motionNegativePrompt == "" {
		motionNegativePrompt = legacyVideoMotionNegative
	}
	seconds := req.Seconds
	if seconds == 0 {
		seconds = V1Seconds(req.DurationSec)
	}
	firstFramePrompt := ""
	firstFrameNegativePrompt := ""
	if videoImageReferencePresent(req.InputReference) {
		firstFramePrompt = videoParamString(req.Params, "image_edit_prompt")
		firstFrameNegativePrompt = legacyNegativePrompt
		if firstFrameNegativePrompt == "" {
			firstFrameNegativePrompt = legacyVideoFirstFrameNegative
		}
	}
	return V1VideoRequest{
		MotionPrompt:             strings.TrimSpace(req.Prompt),
		MotionNegativePrompt:     motionNegativePrompt,
		FirstFramePrompt:         firstFramePrompt,
		FirstFrameNegativePrompt: firstFrameNegativePrompt,
		Size:                     req.Size,
		Seconds:                  seconds,
		Resolution:               videoParamString(req.Params, "resolution"),
		Quality:                  videoParamString(req.Params, "quality"),
		ActionID:                 req.ActionID,
		SourceImage:              req.InputReference,
		PartnerIdentityImage:     legacyVideoImageReference(req.Params, "input_image_end"),
	}, nil
}

func normalizeVideoActionID(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if !videoActionIDPattern.MatchString(value) {
		return "", fmt.Errorf("action_id must be a kebab-case identifier")
	}
	return value, nil
}

func copyVideoImageReference(params map[string]any, key string, ref V1VideoImageReference) (bool, error) {
	image := strings.TrimSpace(ref.Image)
	imageURL := strings.TrimSpace(ref.ImageURL)
	imageBase64 := strings.TrimSpace(ref.ImageBase64)
	populated := 0
	for _, value := range []string{image, imageURL, imageBase64} {
		if value != "" {
			populated++
		}
	}
	if populated > 1 {
		return false, fmt.Errorf("%s must contain exactly one of image, image_url or image_base64", key)
	}
	if image != "" {
		params[key] = image
		return true, nil
	}
	if imageURL != "" {
		params[key+"_url"] = imageURL
		return true, nil
	}
	if imageBase64 != "" {
		params[key+"_base64"] = imageBase64
		return true, nil
	}
	return false, nil
}
