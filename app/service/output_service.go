package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const outputURLTTL = 24 * time.Hour

func isMissingImageEditAfterError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "no image_edit '_after' output produced")
}

func applyImageEditNoSwapFallback(prompt map[string]any) {
	if nodeSwap, ok := prompt["50"].(map[string]any); ok {
		if inputs, ok := nodeSwap["inputs"].(map[string]any); ok {
			inputs["enabled"] = false
		}
	}
	if nodeAfter, ok := prompt["8"].(map[string]any); ok {
		if inputs, ok := nodeAfter["inputs"].(map[string]any); ok {
			inputs["images"] = []any{"39", 0}
		}
	}
	delete(prompt, "80")
}

func maxInt(a, b int) int {
	if a >= b {
		return a
	}
	return b
}

func isVideoOutputFilename(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp4", ".webm", ".mov", ".mkv", ".avi", ".gif":
		return true
	default:
		return false
	}
}

func (s *Server) downloadOutputs(comfy *ComfyClient, taskID string, cfg WorkflowConfig, historyEntry map[string]any, imageEditAfterNodeIDs []string) ([]TaskOutput, error) {
	outputsAny, ok := historyEntry["outputs"].(map[string]any)
	if !ok {
		return nil, errors.New("comfy history has no outputs")
	}
	storageMode, err := NormalizeStorageMode(s.service.StorageMode)
	if err != nil {
		return nil, err
	}
	if storageMode == StorageModeS3 && s.storage == nil {
		return nil, errors.New("storage_mode=s3 requires object storage")
	}
	afterNodeSet := map[string]struct{}{}
	for _, id := range imageEditAfterNodeIDs {
		id = strings.TrimSpace(id)
		if id != "" {
			afterNodeSet[id] = struct{}{}
		}
	}

	imagesDir := filepath.Join(s.outputDir, "images")
	videosDir := filepath.Join(s.outputDir, "videos")
	tempDir := ""
	if storageMode == StorageModeS3 {
		var err error
		tempDir, err = os.MkdirTemp("", "chat-image-output-*")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(tempDir)
	} else {
		if err := os.MkdirAll(imagesDir, 0o755); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(videosDir, 0o755); err != nil {
			return nil, err
		}
	}

	var out []TaskOutput
	var firstViewErr error
	wantKind := strings.TrimSpace(cfg.OutputKind)
	for nodeID, nodeVal := range outputsAny {
		nodeMap, ok := nodeVal.(map[string]any)
		if !ok {
			continue
		}
		nodeID = strings.TrimSpace(nodeID)
		for key, kind := range map[string]string{"images": "image", "videos": "video", "gifs": "video"} {
			if cfg.Mode == "image_edit" && kind == "image" && len(afterNodeSet) > 0 {
				if _, ok := afterNodeSet[nodeID]; !ok {
					continue
				}
			}
			items, _ := nodeMap[key].([]any)
			for _, item := range items {
				m, ok := item.(map[string]any)
				if !ok {
					continue
				}
				filename := asString(m["filename"])
				subfolder := asString(m["subfolder"])
				fileType := asString(m["type"])
				if filename == "" {
					continue
				}
				outputKind := kind
				if kind == "image" && isVideoOutputFilename(filename) {
					outputKind = "video"
				}
				if wantKind != "" && outputKind != wantKind {
					continue
				}
				if cfg.Mode == "image_edit" && kind == "image" && len(afterNodeSet) == 0 {
					marker := strings.ToLower(filename + " " + subfolder)
					isAfter := strings.Contains(marker, "_after") &&
						!strings.Contains(marker, "_after_swap") &&
						!strings.Contains(marker, "_before")
					if !isAfter {
						continue
					}
				}

				content, err := comfy.ViewFile(filename, subfolder, fileType)
				if err != nil {
					// Don't silently drop an already-produced artifact: a /view failure is a
					// download error, not "no output". Log it and keep the first one so the
					// terminal error reports the real cause instead of "no output files".
					log.Printf("[view fail] task=%s file=%s subfolder=%s: %v", taskID, filename, subfolder, err)
					if firstViewErr == nil {
						firstViewErr = err
					}
					continue
				}

				prepared, err := prepareGeneratedOutputContent(outputKind, filename, content, generatedOutputConverters{
					imageToAVIF:    convertImageBytesToAVIF,
					videoToH264MP4: convertVideoBytesToH264MP4,
				})
				if err != nil {
					return nil, fmt.Errorf("prepare output %s: %w", filename, err)
				}
				ext := prepared.ext
				safeBase := sanitizeName(strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename)))
				if safeBase == "" {
					safeBase = "out"
				}
				safeSubfolder := sanitizeName(strings.Trim(filepath.ToSlash(subfolder), "/"))
				if safeSubfolder != "" && safeSubfolder != "." {
					safeBase = safeSubfolder + "_" + safeBase
				}
				now := time.Now()
				targetName := fmt.Sprintf("%s_%s_%s%s", shortID(taskID), now.Format("150405"), safeBase, ext)
				targetDir := imagesDir
				urlPrefix := "/outputs/images/"
				if outputKind == "video" {
					targetDir = videosDir
					urlPrefix = "/outputs/videos/"
				}
				if storageMode == StorageModeS3 {
					targetDir = tempDir
					urlPrefix = ""
				}
				targetPath := filepath.Join(targetDir, targetName)
				size, werr := writePreparedOutput(prepared, targetPath)
				if prepared.cleanup != nil {
					prepared.cleanup()
				}
				if werr != nil {
					return nil, fmt.Errorf("write output %s: %w", targetPath, werr)
				}
				item := TaskOutput{
					Kind:     outputKind,
					Filename: targetName,
					URL:      urlPrefix + targetName,
					Size:     size,
				}
				if storageMode == StorageModeS3 {
					key := gatewayObjectKey(outputKind, targetName, now)
					f, oerr := os.Open(targetPath)
					if oerr != nil {
						return nil, fmt.Errorf("open output %s for upload: %w", targetPath, oerr)
					}
					uploadCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
					uerr := s.storage.UploadPrivate(uploadCtx, key, f, outputContentType(ext))
					cancel()
					if cerr := f.Close(); uerr == nil {
						uerr = cerr
					}
					if uerr != nil {
						return nil, fmt.Errorf("upload output %s: %w", key, uerr)
					}
					urlCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					url, perr := s.storage.PresignGetObject(urlCtx, key, outputURLTTL)
					cancel()
					if perr != nil {
						return nil, perr
					}
					item.Key = key
					item.URL = url
				}
				out = append(out, item)
			}
		}
	}

	if len(out) == 0 {
		// A /view download failure is not the same as the workflow producing nothing;
		// surface the real download error rather than misreporting it as "no output".
		if firstViewErr != nil {
			return nil, fmt.Errorf("failed to download produced output for workflow=%s: %w", cfg.ID, firstViewErr)
		}
		if cfg.Mode == "image_edit" {
			if len(afterNodeSet) > 0 {
				ids := make([]string, 0, len(afterNodeSet))
				for id := range afterNodeSet {
					ids = append(ids, id)
				}
				sort.Strings(ids)
				return nil, fmt.Errorf(
					"no image_edit '_after' output produced for workflow=%s (expected node_ids=%s, expected_state=%s, available=%s)",
					cfg.ID,
					strings.Join(ids, ","),
					summarizeExpectedOutputNodes(outputsAny, ids),
					summarizeOutputNodes(outputsAny),
				)
			}
			return nil, fmt.Errorf(
				"no image_edit '_after' output produced for workflow=%s (available=%s)",
				cfg.ID,
				summarizeOutputNodes(outputsAny),
			)
		}
		return nil, fmt.Errorf("no output files produced for workflow=%s", cfg.ID)
	}
	return out, nil
}
