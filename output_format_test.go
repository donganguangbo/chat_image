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

func TestPrepareGeneratedOutputContentConvertsImageToAVIF(t *testing.T) {
	original := []byte("png bytes")
	converted := []byte("avif bytes")
	called := false

	content, ext, err := prepareGeneratedOutputContent("image", "sample.png", original, generatedOutputConverters{
		imageToAVIF: func(got []byte, sourceExt string) ([]byte, error) {
			called = true
			if !bytes.Equal(got, original) {
				t.Fatalf("converter content = %q, want %q", got, original)
			}
			if sourceExt != ".png" {
				t.Fatalf("sourceExt = %q, want .png", sourceExt)
			}
			return converted, nil
		},
	})
	if err != nil {
		t.Fatalf("prepareGeneratedOutputContent: %v", err)
	}
	if !called {
		t.Fatal("converter was not called")
	}
	if ext != ".avif" {
		t.Fatalf("ext = %q, want .avif", ext)
	}
	if !bytes.Equal(content, converted) {
		t.Fatalf("content = %q, want %q", content, converted)
	}
}

func TestPrepareGeneratedOutputContentKeepsExistingAVIF(t *testing.T) {
	original := []byte("already avif")

	content, ext, err := prepareGeneratedOutputContent("image", "sample.avif", original, generatedOutputConverters{
		imageToAVIF: func([]byte, string) ([]byte, error) {
			t.Fatal("converter should not be called for existing AVIF")
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("prepareGeneratedOutputContent: %v", err)
	}
	if ext != ".avif" {
		t.Fatalf("ext = %q, want .avif", ext)
	}
	if !bytes.Equal(content, original) {
		t.Fatalf("content = %q, want %q", content, original)
	}
}

func TestPrepareGeneratedOutputContentConvertsVideoToAV1MP4(t *testing.T) {
	original := []byte("h264 mp4 bytes")
	converted := []byte("av1 mp4 bytes")
	called := false

	content, ext, err := prepareGeneratedOutputContent(
		"video",
		"clip.webm",
		original,
		generatedOutputConverters{
			imageToAVIF: func([]byte, string) ([]byte, error) {
				t.Fatal("image converter should not be called for video")
				return nil, nil
			},
			videoToAV1MP4: func(got []byte, sourceExt string) ([]byte, error) {
				called = true
				if !bytes.Equal(got, original) {
					t.Fatalf("converter content = %q, want %q", got, original)
				}
				if sourceExt != ".webm" {
					t.Fatalf("sourceExt = %q, want .webm", sourceExt)
				}
				return converted, nil
			},
		},
	)
	if err != nil {
		t.Fatalf("prepareGeneratedOutputContent: %v", err)
	}
	if !called {
		t.Fatal("converter was not called")
	}
	if ext != ".mp4" {
		t.Fatalf("ext = %q, want .mp4", ext)
	}
	if !bytes.Equal(content, converted) {
		t.Fatalf("content = %q, want %q", content, converted)
	}
}

func TestPrepareGeneratedOutputContentReturnsConversionError(t *testing.T) {
	_, _, err := prepareGeneratedOutputContent("image", "sample.png", []byte("bad image"), generatedOutputConverters{
		imageToAVIF: func([]byte, string) ([]byte, error) {
			return nil, errors.New("encoder failed")
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

	converted, err := convertImageBytesToAVIF(pngBytes.Bytes(), ".png")
	if err != nil {
		t.Fatalf("convertImageBytesToAVIF: %v", err)
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

	converted, err := convertVideoBytesToAV1MP4(source, ".mp4")
	if err != nil {
		t.Fatalf("convertVideoBytesToAV1MP4: %v", err)
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
