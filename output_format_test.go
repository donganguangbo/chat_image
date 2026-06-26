package main

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// readPrepared returns the bytes of a preparedOutput, whether it is held in
// memory (passthrough) or written to a temp file (converted).
func readPrepared(t *testing.T, p preparedOutput) []byte {
	t.Helper()
	if p.path != "" {
		b, err := os.ReadFile(p.path)
		if err != nil {
			t.Fatalf("read prepared path: %v", err)
		}
		return b
	}
	return p.data
}

// fileConverter returns a converter that writes out to a temp file and returns
// its path, mimicking the real (streaming) converter contract.
func fileConverter(t *testing.T, out []byte, wantContent []byte, wantExt string) generatedMediaConverter {
	return func(got []byte, sourceExt string) (string, func(), error) {
		if wantContent != nil && !bytes.Equal(got, wantContent) {
			t.Fatalf("converter content = %q, want %q", got, wantContent)
		}
		if wantExt != "" && sourceExt != wantExt {
			t.Fatalf("sourceExt = %q, want %q", sourceExt, wantExt)
		}
		dir := t.TempDir()
		p := filepath.Join(dir, "out.bin")
		if err := os.WriteFile(p, out, 0o600); err != nil {
			t.Fatalf("write fake output: %v", err)
		}
		return p, func() {}, nil
	}
}

func TestPrepareGeneratedOutputContentConvertsImageToAVIF(t *testing.T) {
	original := []byte("png bytes")
	converted := []byte("avif bytes")

	prepared, err := prepareGeneratedOutputContent("image", "sample.png", original, generatedOutputConverters{
		imageToAVIF: fileConverter(t, converted, original, ".png"),
	})
	if err != nil {
		t.Fatalf("prepareGeneratedOutputContent: %v", err)
	}
	if prepared.ext != ".avif" {
		t.Fatalf("ext = %q, want .avif", prepared.ext)
	}
	if prepared.cleanup == nil {
		t.Fatal("cleanup = nil, want non-nil for converted output")
	}
	if got := readPrepared(t, prepared); !bytes.Equal(got, converted) {
		t.Fatalf("content = %q, want %q", got, converted)
	}
}

func TestPrepareGeneratedOutputContentKeepsExistingAVIF(t *testing.T) {
	original := []byte("already avif")

	prepared, err := prepareGeneratedOutputContent("image", "sample.avif", original, generatedOutputConverters{
		imageToAVIF: func([]byte, string) (string, func(), error) {
			t.Fatal("converter should not be called for existing AVIF")
			return "", nil, nil
		},
	})
	if err != nil {
		t.Fatalf("prepareGeneratedOutputContent: %v", err)
	}
	if prepared.ext != ".avif" {
		t.Fatalf("ext = %q, want .avif", prepared.ext)
	}
	if prepared.path != "" {
		t.Fatalf("path = %q, want empty (passthrough)", prepared.path)
	}
	if !bytes.Equal(prepared.data, original) {
		t.Fatalf("data = %q, want %q", prepared.data, original)
	}
}

func TestPrepareGeneratedOutputContentConvertsVideoToAV1MP4(t *testing.T) {
	original := []byte("h264 mp4 bytes")
	converted := []byte("av1 mp4 bytes")

	prepared, err := prepareGeneratedOutputContent(
		"video",
		"clip.webm",
		original,
		generatedOutputConverters{
			imageToAVIF: func([]byte, string) (string, func(), error) {
				t.Fatal("image converter should not be called for video")
				return "", nil, nil
			},
			videoToAV1MP4: fileConverter(t, converted, original, ".webm"),
		},
	)
	if err != nil {
		t.Fatalf("prepareGeneratedOutputContent: %v", err)
	}
	if prepared.ext != ".mp4" {
		t.Fatalf("ext = %q, want .mp4", prepared.ext)
	}
	if got := readPrepared(t, prepared); !bytes.Equal(got, converted) {
		t.Fatalf("content = %q, want %q", got, converted)
	}
}

func TestPrepareGeneratedOutputContentReturnsConversionError(t *testing.T) {
	_, err := prepareGeneratedOutputContent("image", "sample.png", []byte("bad image"), generatedOutputConverters{
		imageToAVIF: func([]byte, string) (string, func(), error) {
			return "", nil, errors.New("encoder failed")
		},
	})
	if err == nil {
		t.Fatal("err = nil, want conversion error")
	}
	if !strings.Contains(err.Error(), "convert generated image to AVIF") || !strings.Contains(err.Error(), "encoder failed") {
		t.Fatalf("err = %q, want wrapped conversion failure", err.Error())
	}
}

func TestOutputContentTypeUsesImageAVIF(t *testing.T) {
	if got := outputContentType(".avif"); got != "image/avif" {
		t.Fatalf("outputContentType(.avif) = %q, want image/avif", got)
	}
}

func TestWritePreparedOutput(t *testing.T) {
	dir := t.TempDir()

	// passthrough (in-memory data)
	target1 := filepath.Join(dir, "passthrough.bin")
	n, err := writePreparedOutput(preparedOutput{data: []byte("hello")}, target1)
	if err != nil {
		t.Fatalf("writePreparedOutput(data): %v", err)
	}
	if n != 5 {
		t.Fatalf("size = %d, want 5", n)
	}
	if got, _ := os.ReadFile(target1); string(got) != "hello" {
		t.Fatalf("target content = %q, want hello", got)
	}

	// converted (streamed from a temp file)
	src := filepath.Join(dir, "src.bin")
	if err := os.WriteFile(src, []byte("world!!"), 0o600); err != nil {
		t.Fatalf("seed src: %v", err)
	}
	target2 := filepath.Join(dir, "converted.bin")
	n, err = writePreparedOutput(preparedOutput{path: src}, target2)
	if err != nil {
		t.Fatalf("writePreparedOutput(path): %v", err)
	}
	if n != 7 {
		t.Fatalf("size = %d, want 7", n)
	}
	if got, _ := os.ReadFile(target2); string(got) != "world!!" {
		t.Fatalf("target content = %q, want world!!", got)
	}
}

func TestConvertImageBytesToAVIFWithFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}

	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 20, B: 20, A: 255})
		}
	}
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}

	path, cleanup, err := convertImageBytesToAVIF(pngBytes.Bytes(), ".png")
	if err != nil {
		t.Fatalf("convertImageBytesToAVIF: %v", err)
	}
	defer cleanup()
	converted, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read converted: %v", err)
	}
	if !bytes.Contains(converted, []byte("ftypavif")) {
		t.Fatalf("converted bytes do not look like AVIF: first bytes % x", converted[:min(len(converted), 16)])
	}
}

func TestConvertVideoBytesToAV1MP4WithFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	if !ffmpegHasAnyEncoder(t, "libsvtav1", "libaom-av1", "librav1e") {
		t.Skip("ffmpeg has no supported AV1 encoder")
	}

	tmpDir := t.TempDir()
	sourcePath := filepath.Join(tmpDir, "source.mp4")
	cmd := exec.Command(
		"ffmpeg",
		"-hide_banner",
		"-loglevel", "error",
		"-y",
		"-f", "lavfi",
		"-i", "testsrc2=s=64x64:r=5:d=1",
		"-c:v", "mpeg4",
		"-q:v", "5",
		sourcePath,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate source mp4: %v: %s", err, strings.TrimSpace(string(output)))
	}
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read source mp4: %v", err)
	}

	path, cleanup, err := convertVideoBytesToAV1MP4(source, ".mp4")
	if err != nil {
		t.Fatalf("convertVideoBytesToAV1MP4: %v", err)
	}
	defer cleanup()
	converted, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read converted: %v", err)
	}
	if !bytes.Contains(converted, []byte("ftyp")) {
		t.Fatalf("converted bytes do not look like MP4: first bytes % x", converted[:min(len(converted), 16)])
	}
	if !bytes.Contains(converted, []byte("av01")) {
		t.Fatalf("converted MP4 does not advertise AV1 sample entry")
	}
}

func ffmpegHasAnyEncoder(t *testing.T, names ...string) bool {
	t.Helper()
	output, err := exec.Command("ffmpeg", "-hide_banner", "-encoders").CombinedOutput()
	if err != nil {
		t.Fatalf("list ffmpeg encoders: %v: %s", err, strings.TrimSpace(string(output)))
	}
	for _, name := range names {
		if bytes.Contains(output, []byte(name)) {
			return true
		}
	}
	return false
}
