package service

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"mime"
	"os"
	"path/filepath"
	"strings"
)

// migrateOutputsToS3 uploads every file under {outputDir}/images and
// {outputDir}/videos to the bucket, keyed exactly like new gateway outputs
// (images|videos/{yyyymmdd(file mtime, UTC)}/{name}) via gatewayObjectKey, so the
// gateway sweep and the DB-ref backfill never produce divergent keys. It reads
// the DB nothing and never writes to disk, so it is safe to run alongside a live
// gateway. dry=true logs the keys without uploading.
func migrateOutputsToS3(ctx context.Context, storage *ObjectStorage, outputDir string, dry bool) error {
	dirs := []struct{ sub, kind string }{{"images", "image"}, {"videos", "video"}}
	var total, uploaded, skipped, failed int
	for _, d := range dirs {
		dir := filepath.Join(outputDir, d.sub)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				log.Printf("[migrate] %s not present, skipping", dir)
				continue
			}
			return fmt.Errorf("read dir %s: %w", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			info, err := e.Info()
			if err != nil {
				log.Printf("[migrate] stat %s/%s failed: %v", d.sub, e.Name(), err)
				failed++
				continue
			}
			name := e.Name()
			key := gatewayObjectKey(d.kind, name, info.ModTime())
			total++
			if dry {
				log.Printf("[migrate:dry] %s/%s -> %s", d.sub, name, key)
				continue
			}
			ok, err := storage.Exists(ctx, key)
			if err != nil {
				log.Printf("[migrate] head %s failed: %v", key, err)
				failed++
				continue
			}
			if ok {
				skipped++
				continue
			}
			content, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				log.Printf("[migrate] read %s/%s failed: %v", d.sub, name, err)
				failed++
				continue
			}
			ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
			if err := storage.UploadPrivate(ctx, key, bytes.NewReader(content), ct); err != nil {
				log.Printf("[migrate] upload %s failed: %v", key, err)
				failed++
				continue
			}
			uploaded++
			if uploaded%50 == 0 {
				log.Printf("[migrate] progress: uploaded=%d skipped=%d failed=%d", uploaded, skipped, failed)
			}
		}
	}
	suffix := ""
	if dry {
		suffix = " (dry-run; nothing uploaded)"
	}
	log.Printf("[migrate] done. total=%d uploaded=%d skipped(existing)=%d failed=%d%s",
		total, uploaded, skipped, failed, suffix)
	if failed > 0 {
		return fmt.Errorf("%d file(s) failed to migrate", failed)
	}
	return nil
}

// migrateTranscodeAv1ToH264 walks the bucket's video objects and, for every one
// whose video stream is AV1, re-encodes it to H.264 (convertVideoBytesToH264MP4)
// and overwrites the SAME key so every DB reference stays valid. Already-H.264
// videos are left untouched, so the pass is idempotent and safe to re-run.
// dry=true only reports what it would convert.
func migrateTranscodeAv1ToH264(ctx context.Context, storage *ObjectStorage, dry bool) error {
	prefixes := []string{"videos/", "chat/"}
	videoExt := map[string]bool{".mp4": true, ".webm": true, ".mov": true, ".mkv": true}
	var total, av1, converted, skipped, failed int
	seen := map[string]bool{}
	for _, prefix := range prefixes {
		keys, err := storage.ListObjects(ctx, prefix)
		if err != nil {
			return err
		}
		for _, key := range keys {
			if seen[key] {
				continue
			}
			seen[key] = true
			ext := strings.ToLower(filepath.Ext(key))
			if !videoExt[ext] {
				continue
			}
			total++
			content, err := storage.Download(ctx, key)
			if err != nil {
				log.Printf("[transcode] download %s failed: %v", key, err)
				failed++
				continue
			}
			codec, err := probeVideoCodec(content, ext)
			if err != nil {
				log.Printf("[transcode] probe %s failed: %v", key, err)
				failed++
				continue
			}
			if codec != "av1" {
				skipped++
				continue
			}
			av1++
			if dry {
				log.Printf("[transcode:dry] %s codec=av1 -> h264", key)
				continue
			}
			outPath, cleanup, err := convertVideoBytesToH264MP4(content, ext)
			if err != nil {
				log.Printf("[transcode] convert %s failed: %v", key, err)
				failed++
				continue
			}
			f, err := os.Open(outPath)
			if err != nil {
				cleanup()
				log.Printf("[transcode] open converted %s failed: %v", key, err)
				failed++
				continue
			}
			uerr := storage.UploadPrivate(ctx, key, f, "video/mp4")
			f.Close()
			cleanup()
			if uerr != nil {
				log.Printf("[transcode] upload %s failed: %v", key, uerr)
				failed++
				continue
			}
			converted++
			log.Printf("[transcode] %s: av1 -> h264 (converted=%d)", key, converted)
		}
	}
	suffix := ""
	if dry {
		suffix = " (dry-run; nothing changed)"
	}
	log.Printf("[transcode] done. videos=%d av1=%d converted=%d skipped(non-av1)=%d failed=%d%s",
		total, av1, converted, skipped, failed, suffix)
	if failed > 0 {
		return fmt.Errorf("%d video(s) failed to transcode", failed)
	}
	return nil
}
