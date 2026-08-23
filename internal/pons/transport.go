package pons

import (
	"bytes"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"
)

// Rate-limit rejections (HTTP 429 or an in-band "request limit reached" body)
// mean the node refused the request before executing it, so replaying is safe
// for every method including eth_sendRawTransaction. Retrying here, once, for
// the whole client means no caller — strategy engine, monitor, funding engine —
// can be killed by a momentary burst over the provider's requests-per-second
// cap; the request just lands a few hundred milliseconds later.
//
// Genuine network failures (timeouts, resets) are NOT retried here: the node
// may have executed those requests, and only the caller knows whether a replay
// is safe.
const (
	rateLimitRetries      = 4
	rateLimitFirstBackoff = 250 * time.Millisecond
	// Bodies larger than this are real payloads (logs, receipts), never
	// rate-limit error envelopes; skip sniffing to avoid buffering them.
	rateLimitSniffLimit = 4096
)

type retryTransport struct {
	base http.RoundTripper
}

func newRetryHTTPClient() *http.Client {
	return &http.Client{Transport: retryTransport{base: http.DefaultTransport}}
}

func (t retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		var err error
		if body, err = io.ReadAll(req.Body); err != nil {
			return nil, err
		}
		req.Body.Close()
	}
	backoff := rateLimitFirstBackoff
	for attempt := 0; ; attempt++ {
		if req.Body != nil {
			req.Body = io.NopCloser(bytes.NewReader(body))
		}
		resp, err := t.base.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		limited, resp, err := sniffRateLimited(resp)
		if err != nil {
			return nil, err
		}
		if !limited || attempt >= rateLimitRetries {
			return resp, nil
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		jitter := time.Duration(rand.Int63n(int64(backoff) / 2))
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(backoff + jitter):
		}
		backoff *= 2
	}
}

// sniffRateLimited reports whether the response is a rate-limit rejection.
// In-band JSON-RPC errors arrive with status 200, so small bodies are read and
// re-buffered onto the returned response for the caller.
func sniffRateLimited(resp *http.Response) (bool, *http.Response, error) {
	if resp.StatusCode == http.StatusTooManyRequests {
		return true, resp, nil
	}
	if resp.Body == nil || (resp.ContentLength > rateLimitSniffLimit) {
		return false, resp, nil
	}
	head := make([]byte, rateLimitSniffLimit+1)
	n, err := io.ReadFull(resp.Body, head)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		resp.Body.Close()
		return false, nil, err
	}
	rest := resp.Body
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(head[:n]), rest), rest}
	if n > rateLimitSniffLimit {
		return false, resp, nil // large payload: real data, not an error envelope
	}
	return isRateLimitMessage(string(head[:n])), resp, nil
}

func isRateLimitMessage(body string) bool {
	if !strings.Contains(body, `"error"`) {
		return false
	}
	lower := strings.ToLower(body)
	for _, needle := range []string{
		"request limit reached", "rate limit", "too many requests",
		"exceeded the quota", "requests per second",
	} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}
