package service

import (
	"errors"
	"time"
)

var ErrQueueFull = errors.New("task queue is full")

const (
	taskRetention = time.Hour

	// maxRequestBody caps the body size in withHMACAuth (which must buffer the
	// whole body for HMAC verification).  The largest legal payload is an i2v
	// request with two base64-encoded images (source_image_base64 +
	// partner_identity_image_base64), each up to 50 MB decoded ≈ 67 MB base64,
	// totalling ≈ 134 MB plus JSON framing.  160 MB is a comfortable ceiling
	// while still bounding unlimited / multi-GB attack bodies.
	maxRequestBody = 160 << 20 // 160 MB
)
