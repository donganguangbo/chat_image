package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ffmpeg run timeouts: a hung/pathologically-slow encode must not pin a worker
// indefinitely. Image is a single frame (fast); video may legitimately take a
// few minutes. On deadline the process is killed (CommandContext) and we fall
// through to the next codec attempt / a clear failure.
const (
	ffmpegImageTimeout = 60 * time.Second
	ffmpegVideoTimeout = 5 * time.Minute
)

// runFFmpeg executes ffmpeg with a hard timeout, returning combined output.
func runFFmpeg(timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ffmpeg", args...).CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("ffmpeg timed out after %s", timeout)
	}
	return out, err
}

// generatedMediaConverter transcodes content and returns the path of the
// result file plus a cleanup func (removes the temp dir). The result stays on
// disk so it can be streamed rather than re-buffered in memory.
type generatedMediaConverter func(content []byte, sourceExt string) (path string, cleanup func(), err error)

type generatedOutputConverters struct {
	imageToAVIF    generatedMediaConverter
	videoToH264MP4 generatedMediaConverter
}

// preparedOutput is either an on-disk converted file (path + cleanup) or
// passthrough bytes (data). Exactly one of path/data is set.
type preparedOutput struct {
	ext     string
	data    []byte // passthrough (e.g. already-AVIF, or non-image/video)
	path    string // converted media on disk
	cleanup func() // non-nil iff path != ""
}

func prepareGeneratedOutputContent(kind, filename string, content []byte, converters generatedOutputConverters) (preparedOutput, error) {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		ext = ".bin"
	}
	switch kind {
	case "image":
		if ext == ".avif" {
			return preparedOutput{ext: ".avif", data: content}, nil
		}
		if converters.imageToAVIF == nil {
			return preparedOutput{}, errors.New("AVIF converter is not configured")
		}
		path, cleanup, err := converters.imageToAVIF(content, ext)
		if err != nil {
			return preparedOutput{}, fmt.Errorf("convert generated image to AVIF: %w", err)
		}
		return preparedOutput{ext: ".avif", path: path, cleanup: cleanup}, nil
	case "video":
		if converters.videoToH264MP4 == nil {
			return preparedOutput{}, errors.New("H.264 video converter is not configured")
		}
		path, cleanup, err := converters.videoToH264MP4(content, ext)
		if err != nil {
			return preparedOutput{}, fmt.Errorf("convert generated video to H.264 MP4: %w", err)
		}
		return preparedOutput{ext: ".mp4", path: path, cleanup: cleanup}, nil
	default:
		return preparedOutput{ext: ext, data: content}, nil
	}
}

// writePreparedOutput writes p to targetPath and returns the bytes written. For
// converted media it streams the temp file (io.Copy, not rename, so it works
// across filesystems); for passthrough it writes the in-memory bytes.
func writePreparedOutput(p preparedOutput, targetPath string) (int64, error) {
	if p.path != "" {
		src, err := os.Open(p.path)
		if err != nil {
			return 0, err
		}
		defer src.Close()
		dst, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return 0, err
		}
		n, err := io.Copy(dst, src)
		if cerr := dst.Close(); err == nil {
			err = cerr
		}
		return n, err
	}
	if err := os.WriteFile(targetPath, p.data, 0o644); err != nil {
		return 0, err
	}
	return int64(len(p.data)), nil
}

// convertImageBytesToAVIF transcodes content to AVIF and returns the path of the
// result file plus a cleanup func that removes its temp dir. On error it cleans
// up itself and returns ("", nil, err). The output is left on disk (not read
// back into memory) so the caller can stream it to its target / object storage.
func convertImageBytesToAVIF(content []byte, sourceExt string) (string, func(), error) {
	if len(content) == 0 {
		return "", nil, errors.New("empty image content")
	}

	tmpDir, err := os.MkdirTemp("", "go_gateway_avif_*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }

	inputPath := filepath.Join(tmpDir, "input"+sanitizeMediaInputExt(sourceExt))
	outputPath := filepath.Join(tmpDir, "output.avif")
	if err := os.WriteFile(inputPath, content, 0o600); err != nil {
		cleanup()
		return "", nil, err
	}

	baseArgs := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-y",
		"-i", inputPath,
		"-frames:v", "1",
		"-an",
		"-pix_fmt", "yuv420p",
	}
	attempts := []struct {
		name string
		args []string
	}{
		{"libsvtav1", append(append([]string{}, baseArgs...), "-c:v", "libsvtav1", "-crf", "30", "-preset", "8", outputPath)},
		{"libaom-av1", append(append([]string{}, baseArgs...), "-c:v", "libaom-av1", "-crf", "30", "-b:v", "0", "-cpu-used", "6", outputPath)},
		{"default-av1", append(append([]string{}, baseArgs...), "-crf", "30", outputPath)},
	}
	var failures []string
	for _, attempt := range attempts {
		_ = os.Remove(outputPath)
		output, err := runFFmpeg(ffmpegImageTimeout, attempt.args...)
		if err != nil {
			msg := strings.TrimSpace(string(output))
			if msg == "" {
				msg = err.Error()
			}
			failures = append(failures, fmt.Sprintf("%s: %s", attempt.name, truncate(msg, 1024)))
			continue
		}

		if fi, err := os.Stat(outputPath); err != nil || fi.Size() == 0 {
			failures = append(failures, fmt.Sprintf("%s: produced empty/missing AVIF", attempt.name))
			continue
		}
		return outputPath, cleanup, nil
	}
	cleanup()
	return "", nil, fmt.Errorf("ffmpeg AVIF conversion failed: %s", strings.Join(failures, "; "))
}

// convertVideoBytesToH264MP4 transcodes to H.264 / AAC-less MP4 (yuv420p +
// faststart), the most universally playable format (every browser, iOS/Safari,
// Android, older devices) — AV1 was rejected for poor playback compatibility.
// It returns the output file path + cleanup func and leaves the encoded MP4 on
// disk rather than reading it back into memory.
func convertVideoBytesToH264MP4(content []byte, sourceExt string) (string, func(), error) {
	if len(content) == 0 {
		return "", nil, errors.New("empty video content")
	}

	tmpDir, err := os.MkdirTemp("", "go_gateway_h264_video_*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }

	inputPath := filepath.Join(tmpDir, "input"+sanitizeMediaInputExt(sourceExt))
	outputPath := filepath.Join(tmpDir, "output.mp4")
	if err := os.WriteFile(inputPath, content, 0o600); err != nil {
		cleanup()
		return "", nil, err
	}

	baseArgs := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-y",
		"-i", inputPath,
		"-map", "0:v:0",
		"-an",
		"-pix_fmt", "yuv420p", // 8-bit 4:2:0 — required for broad hardware decode
		"-movflags", "+faststart",
	}
	// libx264 (main profile) is the gold standard for compatibility; h264_nvenc is
	// a GPU-accelerated fallback; bare "h264" lets ffmpeg pick any built-in H.264
	// encoder as a last resort.
	attempts := []struct {
		name string
		args []string
	}{
		{"libx264", append(append([]string{}, baseArgs...), "-c:v", "libx264", "-crf", "23", "-preset", "medium", "-profile:v", "main", outputPath)},
		{"h264_nvenc", append(append([]string{}, baseArgs...), "-c:v", "h264_nvenc", "-preset", "p5", "-cq", "23", "-profile:v", "main", outputPath)},
		{"default-h264", append(append([]string{}, baseArgs...), "-c:v", "h264", outputPath)},
	}
	var failures []string
	for _, attempt := range attempts {
		_ = os.Remove(outputPath)
		output, err := runFFmpeg(ffmpegVideoTimeout, attempt.args...)
		if err != nil {
			msg := strings.TrimSpace(string(output))
			if msg == "" {
				msg = err.Error()
			}
			failures = append(failures, fmt.Sprintf("%s: %s", attempt.name, truncate(msg, 1024)))
			continue
		}

		if fi, err := os.Stat(outputPath); err != nil || fi.Size() == 0 {
			failures = append(failures, fmt.Sprintf("%s: produced empty/missing MP4", attempt.name))
			continue
		}
		return outputPath, cleanup, nil
	}
	cleanup()
	return "", nil, fmt.Errorf("ffmpeg H.264 MP4 conversion failed: %s", strings.Join(failures, "; "))
}

func sanitizeMediaInputExt(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	if ext == "" || ext == ".bin" {
		return ".img"
	}
	if !strings.HasPrefix(ext, ".") || strings.ContainsAny(ext, `/\:`) {
		return ".img"
	}
	return ext
}

func outputContentType(ext string) string {
	if strings.EqualFold(strings.TrimSpace(ext), ".avif") {
		return "image/avif"
	}
	return mime.TypeByExtension(strings.ToLower(strings.TrimSpace(ext)))
}
