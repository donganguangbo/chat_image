package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// authTestHandler returns the middleware under test wrapped around a handler
// that records whether the request got through.
func authTestHandler(t *testing.T, secret, bearer string) (http.Handler, *bool) {
	t.Helper()
	passed := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		passed = true
		w.WriteHeader(http.StatusOK)
	})
	return withHMACAuth(next, secret, bearer, newNonceCache(), newByteBudget(maxRequestBody*4)), &passed
}

// signedRequest builds a request carrying a valid HMAC signature.
func signedRequest(t *testing.T, secret, method, path, body string) *http.Request {
	t.Helper()
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := hex.EncodeToString([]byte(time.Now().Format("150405.000000000")))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(method + "\n" + path + "\n" + ts + "\n" + nonce + "\n" + body))
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(hmacTimestampHeader, ts)
	req.Header.Set(hmacNonceHeader, nonce)
	req.Header.Set(hmacSignatureHeader, hex.EncodeToString(mac.Sum(nil)))
	return req
}

// TestBearerAccepted a correct bearer token gets through without any HMAC
// headers — this is the whole point: upstream gateways cannot sign requests.
func TestBearerAccepted(t *testing.T) {
	h, passed := authTestHandler(t, "hmac-secret", "gw-token")
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"prompt":"x"}`))
	req.Header.Set(authorizationHeader, bearerPrefix+"gw-token")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !*passed {
		t.Fatalf("valid bearer should pass: code=%d passed=%v", rec.Code, *passed)
	}
}

// TestBearerRejectedWhenWrong a wrong token must not fall through to the HMAC
// path — otherwise a caller could probe tokens and then retry as unsigned.
func TestBearerRejectedWhenWrong(t *testing.T) {
	h, passed := authTestHandler(t, "hmac-secret", "gw-token")
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader("{}"))
	req.Header.Set(authorizationHeader, bearerPrefix+"wrong")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || *passed {
		t.Fatalf("wrong bearer must be rejected: code=%d passed=%v", rec.Code, *passed)
	}
}

// TestBearerDisabledByDefault with no token configured, an Authorization
// header must not authenticate anything — enabling the weaker scheme has to be
// an explicit deployment choice.
func TestBearerDisabledByDefault(t *testing.T) {
	h, passed := authTestHandler(t, "hmac-secret", "")
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader("{}"))
	req.Header.Set(authorizationHeader, bearerPrefix+"anything")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || *passed {
		t.Fatalf("bearer must be inert when unconfigured: code=%d passed=%v", rec.Code, *passed)
	}
}

// TestHMACStillWorksAlongsideBearer enabling bearer must not be a migration:
// existing signing clients keep working untouched.
func TestHMACStillWorksAlongsideBearer(t *testing.T) {
	const secret = "hmac-secret"
	h, passed := authTestHandler(t, secret, "gw-token")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, signedRequest(t, secret, http.MethodPost, "/v1/images/generations", `{"prompt":"x"}`))
	if rec.Code != http.StatusOK || !*passed {
		t.Fatalf("HMAC path must keep working: code=%d passed=%v", rec.Code, *passed)
	}
}

// TestBadSignatureStillRejectedWithBearerOn the bearer path must not weaken the
// HMAC path for requests that do attempt to sign.
func TestBadSignatureStillRejectedWithBearerOn(t *testing.T) {
	h, passed := authTestHandler(t, "hmac-secret", "gw-token")
	req := signedRequest(t, "wrong-secret", http.MethodPost, "/v1/images/generations", "{}")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || *passed {
		t.Fatalf("bad signature must still be rejected: code=%d passed=%v", rec.Code, *passed)
	}
}

// TestUnauthenticatedRejected no credentials at all.
func TestUnauthenticatedRejected(t *testing.T) {
	h, passed := authTestHandler(t, "hmac-secret", "gw-token")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader("{}")))
	if rec.Code != http.StatusUnauthorized || *passed {
		t.Fatalf("unauthenticated request must be rejected: code=%d passed=%v", rec.Code, *passed)
	}
}

// TestHealthzStillBypasses monitors have no credentials.
func TestHealthzStillBypasses(t *testing.T) {
	h, passed := authTestHandler(t, "hmac-secret", "gw-token")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || !*passed {
		t.Fatalf("/healthz must bypass auth: code=%d passed=%v", rec.Code, *passed)
	}
}
