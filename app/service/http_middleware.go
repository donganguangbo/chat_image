package service

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// authRejectLimiter rate-limits auth-reject logging. Every reject path is
// reachable pre-signature by an unauthenticated client, so an un-throttled
// log.Printf per reject is itself a DoS lever (log-mutex contention + disk
// fill). We cap it and count the suppressed remainder.
var authRejectLimiter = struct {
	mu       sync.Mutex
	windowAt time.Time
	count    int
	dropped  int
}{}

// logAuthReject records a rejected request at the auth boundary. Auth failures
// (missing/expired signature, replayed nonce, key/clock mismatch) are otherwise
// invisible on the gateway — a whole batch 401-ing looks like "service down".
// Never logs the signature or secret. Rate-limited to bound log amplification.
func logAuthReject(r *http.Request, reason string) {
	const window = time.Second
	const perWindow = 20
	authRejectLimiter.mu.Lock()
	now := time.Now()
	flushDropped := 0
	if now.Sub(authRejectLimiter.windowAt) >= window {
		flushDropped = authRejectLimiter.dropped
		authRejectLimiter.windowAt = now
		authRejectLimiter.count = 0
		authRejectLimiter.dropped = 0
	}
	allow := authRejectLimiter.count < perWindow
	if allow {
		authRejectLimiter.count++
	} else {
		authRejectLimiter.dropped++
	}
	authRejectLimiter.mu.Unlock()

	// Log outside the lock so a slow logger can't serialize request goroutines.
	if flushDropped > 0 {
		log.Printf("[auth reject] suppressed %d more in the last %s", flushDropped, window)
	}
	if allow {
		log.Printf("[auth reject] reason=%q method=%s path=%s remote=%s", reason, r.Method, r.URL.Path, r.RemoteAddr)
	}
}

// byteBudget bounds the TOTAL bytes being buffered concurrently (not a request
// count), so many small bodies can't sum past the cap the way a count-only
// semaphore would allow. nil means unbounded.
type byteBudget struct {
	mu   sync.Mutex
	used int64
	max  int64
}

func newByteBudget(max int64) *byteBudget {
	if max <= 0 {
		return nil
	}
	return &byteBudget{max: max}
}

func (b *byteBudget) acquire(n int64) bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used+n > b.max {
		return false
	}
	b.used += n
	return true
}

func (b *byteBudget) release(n int64) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.used -= n
	b.mu.Unlock()
}

// bodyReadIdleTimeout is the IDLE (not absolute) read timeout for buffering a
// request body: it is refreshed after every successful read, so a body that
// keeps making progress is never cut off no matter how large, while a stalled
// read is aborted quickly. This is what stops a slow-trickle attacker from
// holding a byte-budget reservation for the whole (size-scaled) transfer window.
const bodyReadIdleTimeout = 20 * time.Second

// readAllIdle reads all of body under an idle deadline enforced via rc. A read
// that makes no progress for bodyReadIdleTimeout is aborted; progress resets the
// clock. Total buffered bytes are still bounded by the MaxBytesReader wrapping
// body (returns an error past the cap).
func readAllIdle(rc *http.ResponseController, body io.Reader, idle time.Duration) ([]byte, error) {
	buf := make([]byte, 0, 64*1024)
	chunk := make([]byte, 256*1024)
	for {
		if rc != nil {
			_ = rc.SetReadDeadline(time.Now().Add(idle))
		}
		n, err := body.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
		}
		if err != nil {
			if err == io.EOF {
				return buf, nil
			}
			return buf, err
		}
	}
}

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
//
// Bearer alternative (for upstream gateways)
//
// When GATEWAY_BEARER_TOKEN is set, a request may instead present
//   Authorization: Bearer <token>
// and skip the HMAC dance. This exists for one reason: model gateways such as
// new-api can only send a static bearer token per channel — they have no hook
// to compute a per-request signature. Without this, fronting this service with
// such a gateway would require a signing proxy: another process to deploy,
// monitor and keep in sync, existing solely to translate one auth scheme into
// another.
//
// The trade-off is explicit: a bearer token is replayable if the channel is
// observed, while the HMAC scheme is not. That is acceptable only because the
// intended deployment is gateway→service over a private network. **Do not
// enable it on a publicly reachable instance** — keep HMAC there.
//
// Both schemes stay live simultaneously: existing HMAC clients keep working
// unchanged, so enabling this is not a migration.
// ─────────────────────────────────────────────────────────────────────────────

const (
	hmacTimestampHeader = "X-Gateway-Timestamp"
	hmacNonceHeader     = "X-Gateway-Nonce"
	hmacSignatureHeader = "X-Gateway-Signature"
	hmacTimeWindowSec   = 300

	authorizationHeader = "Authorization"
	bearerPrefix        = "Bearer "
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
	// Health/readiness probes (k8s, uptime monitors) need no auth.
	if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
		return true
	}
	// NOTE: /outputs/* used to bypass; it no longer does. chat-web fronts all
	// output access via /api/media/* and adds the HMAC signature server-side,
	// so end users never hit the gateway directly. See chat-web's
	// src/app/api/media/[...path]/route.ts.
	return false
}

func withHMACAuth(next http.Handler, secret, bearerToken string, nc *nonceCache, bodyBudget *byteBudget) http.Handler {
	secretBytes := []byte(secret)
	bearerBytes := []byte(bearerToken)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bypassAuth(r) {
			next.ServeHTTP(w, r)
			return
		}

		// Bearer path (see the block comment above): checked before the HMAC
		// path because it needs neither the body nor a nonce — an upstream
		// gateway's request should not be buffered just to be authenticated.
		if len(bearerBytes) > 0 {
			if presented, ok := strings.CutPrefix(r.Header.Get(authorizationHeader), bearerPrefix); ok {
				// Constant-time compare: a byte-by-byte early exit would leak
				// the token one character at a time under timing analysis.
				if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(presented)), bearerBytes) == 1 {
					next.ServeHTTP(w, r)
					return
				}
				logAuthReject(r, "invalid bearer token")
				http.Error(w, "unauthorized: invalid bearer token", http.StatusUnauthorized)
				return
			}
			// No bearer header at all → fall through to HMAC, so both schemes
			// keep working side by side.
		}

		tsStr := r.Header.Get(hmacTimestampHeader)
		nonce := r.Header.Get(hmacNonceHeader)
		sigHex := r.Header.Get(hmacSignatureHeader)
		if tsStr == "" || nonce == "" || sigHex == "" {
			logAuthReject(r, "missing signature headers")
			http.Error(w, "unauthorized: missing signature headers", http.StatusUnauthorized)
			return
		}

		ts, err := strconv.ParseInt(tsStr, 10, 64)
		if err != nil {
			logAuthReject(r, "invalid timestamp")
			http.Error(w, "unauthorized: invalid timestamp", http.StatusUnauthorized)
			return
		}
		now := time.Now().Unix()
		if ts < now-hmacTimeWindowSec || ts > now+hmacTimeWindowSec {
			logAuthReject(r, "timestamp out of window")
			http.Error(w, "unauthorized: timestamp out of window", http.StatusUnauthorized)
			return
		}

		if !nc.claim(nonce, now) {
			logAuthReject(r, "nonce reused")
			http.Error(w, "unauthorized: nonce reused", http.StatusUnauthorized)
			return
		}

		// Read body once; preserve it for the downstream handler. HMAC requires
		// the full body in memory, and the signature isn't verified until AFTER
		// this read — so an unauthenticated attacker can force it with ANY verb.
		// Defenses, all scoped to just the buffering window:
		//   - MaxBytesReader caps a single body at maxRequestBody.
		//   - bodyBudget bounds the TOTAL bytes buffered concurrently across all
		//     in-flight requests, so neither a few huge bodies nor a flood of
		//     small ones can exhaust memory (a request-count cap couldn't stop the
		//     latter). Empty/small requests (status polls) reserve ~0 and never
		//     block; the reservation is freed the instant the read finishes, so
		//     long-lived streaming handlers never hold it.
		//   - a size-scaled read deadline stops a slow-trickle body from pinning
		//     a reservation.
		var body []byte
		if r.Body != nil {
			reserve := r.ContentLength
			if reserve < 0 || reserve > maxRequestBody {
				reserve = maxRequestBody // unknown length (chunked) → reserve worst case
			}
			if !bodyBudget.acquire(reserve) {
				http.Error(w, "server busy: too much concurrent request body in flight", http.StatusServiceUnavailable)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
			rc := http.NewResponseController(w)
			var readErr error
			body, readErr = readAllIdle(rc, r.Body, bodyReadIdleTimeout)
			_ = rc.SetReadDeadline(time.Time{}) // clear; don't bound the handler's own reads
			bodyBudget.release(reserve)
			if readErr != nil {
				logAuthReject(r, "body read failed / too large / too slow")
				http.Error(w, "request body too large or too slow", http.StatusRequestEntityTooLarge)
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
			logAuthReject(r, "invalid signature")
			http.Error(w, "unauthorized: invalid signature", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}
