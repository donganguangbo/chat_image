package service

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// isBlockedIP returns true for loopback, private, link-local, and unspecified
// addresses that must not be reachable from user-supplied URLs.
func isBlockedIP(ip net.IP) bool {
	return ip == nil ||
		ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified()
}

// safeHTTPClient is a package-level hardened client used for all outbound
// image downloads. It blocks SSRF targets (cloud metadata, internal services)
// by inspecting the already-resolved IP in DialContext's Control hook, and
// re-validates redirect destinations.
var safeHTTPClient = &http.Client{
	Timeout: 60 * time.Second,
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
			Control: func(network, address string, _ syscall.RawConn) error {
				host, _, err := net.SplitHostPort(address)
				if err != nil {
					return fmt.Errorf("ssrf-guard: cannot parse address %q: %w", address, err)
				}
				ip := net.ParseIP(host)
				if isBlockedIP(ip) {
					return fmt.Errorf("ssrf-guard: address %q is not allowed", address)
				}
				return nil
			},
		}).DialContext,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("ssrf-guard: too many redirects")
		}
		host := req.URL.Hostname()
		if ip := net.ParseIP(host); ip != nil {
			if isBlockedIP(ip) {
				return fmt.Errorf("ssrf-guard: redirect to %q is not allowed", host)
			}
		}
		return nil
	},
}

func (s *Server) materializeImageParam(comfy *ComfyClient, taskID string, params map[string]any, key string) error {
	raw := asString(params[key])
	rawURL := asString(params[key+"_url"])
	rawBase64 := asString(params[key+"_base64"])

	var localPath string

	switch {
	case rawURL != "":
		p, err := downloadImageFromURL(rawURL)
		if err != nil {
			return err
		}
		localPath = p
		defer func() { _ = os.Remove(p) }()
	case rawBase64 != "":
		p, err := decodeBase64ImageToTemp(rawBase64)
		if err != nil {
			return err
		}
		localPath = p
		defer func() { _ = os.Remove(p) }()
	case raw != "":
		// Do not touch the local filesystem; keep original value for
		// Comfy-side input resolution (Comfy resolves against its own input dir).
		return nil
	default:
		return nil
	}

	s.updateTask(taskID, func(t *Task) {
		if t.Progress < 10 {
			t.Progress = 10
		}
		t.Message = "uploading input image"
	})
	uploaded, err := comfy.UploadImage(localPath, "go_gateway_uploads")
	if err != nil {
		return err
	}
	params[key] = uploaded
	return nil
}

func downloadImageFromURL(rawURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", fmt.Errorf("invalid image url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("image url must be http or https")
	}

	// User-supplied remote hosts are the least reliable dependency, so retry a
	// few times on transport errors and 5xx/429 (transient); 4xx is a permanent
	// client error and is not retried. SSRF guards / size limit are unchanged
	// (they live in safeHTTPClient and the read below).
	const maxSize = int64(50 << 20)
	const downloadAttempts = 3
	var payload []byte
	var contentType string
	var lastErr error
	for attempt := 0; attempt < downloadAttempts; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(500*(1<<(attempt-1))) * time.Millisecond // 0.5s, 1s
			log.Printf("[image download retry] url_host=%s attempt=%d err=%v; retrying in %s", u.Hostname(), attempt, lastErr, backoff)
			time.Sleep(backoff)
		}

		req, err := http.NewRequest(http.MethodGet, u.String(), nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("User-Agent", "chat-image-go-gateway/1.0")

		// Use the package-level hardened client (SSRF protection, redirect limits).
		resp, err := safeHTTPClient.Do(req)
		if err != nil {
			lastErr = err
			if isRetryableNetErr(err) {
				continue
			}
			return "", err
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			drainClose(resp.Body)
			lastErr = fmt.Errorf("download image failed: %s %s", resp.Status, strings.TrimSpace(string(raw)))
			if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
				continue
			}
			return "", lastErr
		}

		contentType = resp.Header.Get("Content-Type")
		payload, err = io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
		drainClose(resp.Body)
		if err != nil {
			lastErr = err
			continue
		}
		lastErr = nil
		break
	}
	if lastErr != nil {
		return "", lastErr
	}
	if int64(len(payload)) > maxSize {
		return "", fmt.Errorf("remote image too large: %d bytes", len(payload))
	}
	if len(payload) == 0 {
		return "", errors.New("remote image is empty")
	}

	ext := inferImageExt(contentType, u.Path, payload)
	tmp, err := os.CreateTemp("", "go_gateway_img_*"+ext)
	if err != nil {
		return "", err
	}
	defer tmp.Close()
	if _, err := tmp.Write(payload); err != nil {
		// Fix 3: clean up orphan temp file on write failure.
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}

func decodeBase64ImageToTemp(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("empty base64 image")
	}

	contentType := ""
	if strings.HasPrefix(strings.ToLower(s), "data:") {
		comma := strings.Index(s, ",")
		if comma <= 0 {
			return "", errors.New("invalid data url image")
		}
		head := s[:comma]
		if !strings.Contains(strings.ToLower(head), ";base64") {
			return "", errors.New("data url is not base64")
		}
		parts := strings.SplitN(head, ";", 2)
		contentType = strings.TrimPrefix(parts[0], "data:")
		s = s[comma+1:]
	}

	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		stripped := strings.TrimRight(s, "=")
		var err2 error
		decoded, err2 = base64.RawStdEncoding.DecodeString(stripped)
		if err2 != nil {
			decoded, err2 = base64.RawURLEncoding.DecodeString(stripped)
			if err2 != nil {
				return "", fmt.Errorf("decode base64 image failed: %w", err)
			}
		}
	}
	if len(decoded) == 0 {
		return "", errors.New("base64 image is empty")
	}
	if len(decoded) > 50<<20 {
		return "", fmt.Errorf("base64 image too large: %d bytes", len(decoded))
	}

	ext := inferImageExt(contentType, "", decoded)
	tmp, err := os.CreateTemp("", "go_gateway_img_b64_*"+ext)
	if err != nil {
		return "", err
	}
	defer tmp.Close()
	if _, err := tmp.Write(decoded); err != nil {
		// Fix 3: clean up orphan temp file on write failure.
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}

func inferImageExt(contentType, path string, payload []byte) string {
	if mediaType, _, err := mime.ParseMediaType(contentType); err == nil {
		switch strings.ToLower(mediaType) {
		case "image/png":
			return ".png"
		case "image/jpeg", "image/jpg":
			return ".jpg"
		case "image/webp":
			return ".webp"
		case "image/gif":
			return ".gif"
		case "image/bmp":
			return ".bmp"
		case "image/avif":
			return ".avif"
		}
	}

	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp", ".avif":
		return ext
	}

	switch strings.ToLower(http.DetectContentType(payload)) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "image/bmp":
		return ".bmp"
	}
	return ".png"
}
