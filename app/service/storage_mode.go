package service

import (
	"fmt"
	"strings"
)

const (
	StorageModeLocal = "local"
	StorageModeS3    = "s3"
)

func NormalizeStorageMode(mode string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case StorageModeLocal:
		return StorageModeLocal, nil
	case StorageModeS3:
		return StorageModeS3, nil
	default:
		return "", fmt.Errorf("storage_mode must be %q or %q", StorageModeLocal, StorageModeS3)
	}
}
