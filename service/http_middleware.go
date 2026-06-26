package service

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Gateway-Timestamp, X-Gateway-Nonce, X-Gateway-Signature")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// HMAC request authentication
//
// Every API request must carry three headers:
//   X-Gateway-Timestamp: Unix seconds (string)
//   X-Gateway-Nonce:     hex-encoded random bytes, single use within the window
//   X-Gateway-Signature: hex(HMAC-SHA256(secret, METHOD\n PATH\n TS\n NONCE\n BODY))
//
// The secret never travels on the wire. Even if the channel is observed, an
// attacker cannot forge a new request (no secret) and cannot replay an old one
// (nonce single-use, timestamp ±300s window).
//
// /healthz and /outputs/ bypass auth — health is for monitors; outputs are
// static files served to end-users whose browsers cannot attach headers.
// ─────────────────────────────────────────────────────────────────────────────

const (
	hmacTimestampHeader = "X-Gateway-Timestamp"
	hmacNonceHeader     = "X-Gateway-Nonce"
	hmacSignatureHeader = "X-Gateway-Signature"
	hmacTimeWindowSec   = 300
)

type nonceCache struct {
	mu      sync.Mutex
	entries map[string]int64 // nonce -> unix seconds seen
}

func newNonceCache() *nonceCache {
	nc := &nonceCache{entries: make(map[string]int64)}
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			nc.cleanup()
		}
	}()
	return nc
}

// claim returns true when the nonce was not previously seen within the window
// and is now registered; false means the nonce was already used (replay).
func (nc *nonceCache) claim(nonce string, now int64) bool {
	nc.mu.Lock()
	defer nc.mu.Unlock()
	if seen, ok := nc.entries[nonce]; ok && now-seen <= hmacTimeWindowSec {
		return false
	}
	nc.entries[nonce] = now
	return true
}

func (nc *nonceCache) cleanup() {
	now := time.Now().Unix()
	nc.mu.Lock()
	defer nc.mu.Unlock()
	for k, t := range nc.entries {
		if now-t > hmacTimeWindowSec {
			delete(nc.entries, k)
		}
	}
}

func bypassAuth(r *http.Request) bool {
	// CORS preflight has no body and never carries auth headers in this product;
	// the real request that follows is signed.
	if r.Method == http.MethodOptions {
		return true
	}
	// Health probes (k8s, uptime monitors) need no auth.
	if r.URL.Path == "/healthz" {
		return true
	}
	// NOTE: /outputs/* used to bypass; it no longer does. chat-web fronts all
	// output access via /api/media/* and adds the HMAC signature server-side,
	// so end users never hit the gateway directly. See chat-web's
	// src/app/api/media/[...path]/route.ts.
	return false
}

func withHMACAuth(next http.Handler, secret string, nc *nonceCache) http.Handler {
	secretBytes := []byte(secret)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bypassAuth(r) {
			next.ServeHTTP(w, r)
			return
		}

		tsStr := r.Header.Get(hmacTimestampHeader)
		nonce := r.Header.Get(hmacNonceHeader)
		sigHex := r.Header.Get(hmacSignatureHeader)
		if tsStr == "" || nonce == "" || sigHex == "" {
			http.Error(w, "unauthorized: missing signature headers", http.StatusUnauthorized)
			return
		}

		ts, err := strconv.ParseInt(tsStr, 10, 64)
		if err != nil {
			http.Error(w, "unauthorized: invalid timestamp", http.StatusUnauthorized)
			return
		}
		now := time.Now().Unix()
		if ts < now-hmacTimeWindowSec || ts > now+hmacTimeWindowSec {
			http.Error(w, "unauthorized: timestamp out of window", http.StatusUnauthorized)
			return
		}

		if !nc.claim(nonce, now) {
			http.Error(w, "unauthorized: nonce reused", http.StatusUnauthorized)
			return
		}

		// Read body once; preserve it for the downstream handler.
		// Wrap with MaxBytesReader before reading to prevent OOM on oversized
		// bodies; HMAC requires the full body in memory.
		var body []byte
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
			var readErr error
			body, readErr = io.ReadAll(r.Body)
			if readErr != nil {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}

		path := r.URL.Path
		if r.URL.RawQuery != "" {
			path = path + "?" + r.URL.RawQuery
		}
		msg := r.Method + "\n" + path + "\n" + tsStr + "\n" + nonce + "\n" + string(body)
		mac := hmac.New(sha256.New, secretBytes)
		mac.Write([]byte(msg))
		expected := mac.Sum(nil)

		got, err := hex.DecodeString(sigHex)
		if err != nil || !hmac.Equal(got, expected) {
			http.Error(w, "unauthorized: invalid signature", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}
