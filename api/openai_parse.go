package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

func parseOpenAIImageEditRequest(r *http.Request) (OpenAIImageRequest, error) {
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		if err := r.ParseMultipartForm(maxRequestBody); err != nil {
			return OpenAIImageRequest{}, fmt.Errorf("invalid multipart body")
		}
		if r.MultipartForm != nil {
			defer func() { _ = r.MultipartForm.RemoveAll() }()
		}
		req := OpenAIImageRequest{
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
					return OpenAIImageRequest{}, fmt.Errorf("read image file: %w", err)
				}
				defer f.Close()
				b, err := io.ReadAll(f)
				if err != nil {
					return OpenAIImageRequest{}, fmt.Errorf("read image file: %w", err)
				}
				req.InputImageBase64 = base64.StdEncoding.EncodeToString(b)
			}
			if files := r.MultipartForm.File["mask"]; len(files) > 0 {
				f, err := files[0].Open()
				if err != nil {
					return OpenAIImageRequest{}, fmt.Errorf("read mask file: %w", err)
				}
				defer f.Close()
				b, err := io.ReadAll(f)
				if err != nil {
					return OpenAIImageRequest{}, fmt.Errorf("read mask file: %w", err)
				}
				req.Params["mask_base64"] = base64.StdEncoding.EncodeToString(b)
			}
		}
		return req, nil
	}

	var req OpenAIImageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return OpenAIImageRequest{}, fmt.Errorf("invalid JSON body")
	}
	return req, nil
}

func parseOpenAISize(size string) (int, int, error) {
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

func copyOpenAICommonParams(params map[string]any, model, user string) {
	if model = strings.TrimSpace(model); model != "" {
		params["model"] = model
	}
	if user = strings.TrimSpace(user); user != "" {
		params["user"] = user
	}
}
