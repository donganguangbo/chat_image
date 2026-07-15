package service

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"syscall"
	"testing"
)

// isRetryableNetErr is the QueuePrompt double-queue red line: it must return true
// ONLY for failures that prove the request never reached the server (dial / DNS /
// refused). Anything ambiguous (response timeout, mid-stream reset, EOF) MUST be
// non-retryable, or a retried POST /prompt double-queues a GPU job.
func TestIsRetryableNetErr(t *testing.T) {
	dialRefused := &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},

		// pre-delivery: safe to retry
		{"dial refused", dialRefused, true},
		{"dial timeout", &net.OpError{Op: "dial", Err: errors.New("i/o timeout")}, true},
		{"raw ECONNREFUSED", syscall.ECONNREFUSED, true},
		{"dns no such host", &net.DNSError{Err: "no such host", Name: "comfy", IsNotFound: true}, true},
		// how http.Client.Do actually surfaces a refused dial (wrapped in *url.Error)
		{"url.Error wrapping dial refused", &url.Error{Op: "Post", URL: "http://x/prompt", Err: dialRefused}, true},

		// post-send / ambiguous: MUST NOT retry (server may have enqueued)
		{"bare EOF", io.EOF, false},
		{"read reset", &net.OpError{Op: "read", Err: syscall.ECONNRESET}, false},
		{"write broken pipe", &net.OpError{Op: "write", Err: syscall.EPIPE}, false},
		{"context deadline", context.DeadlineExceeded, false},
		// the key regression: Do returns *url.Error wrapping EOF after the request was sent
		{"url.Error wrapping EOF", &url.Error{Op: "Post", URL: "http://x/prompt", Err: io.EOF}, false},
		{"plain string reset", errors.New("connection reset by peer"), false},
	}
	for _, tc := range cases {
		if got := isRetryableNetErr(tc.err); got != tc.want {
			t.Errorf("%s: isRetryableNetErr(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}
