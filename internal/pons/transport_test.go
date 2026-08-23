package pons

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func doJSONRPC(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url,
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"eth_chainId"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := newRetryHTTPClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(body)
}

func TestRetryTransportRetriesInBandRateLimit(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if body, _ := io.ReadAll(r.Body); !strings.Contains(string(body), "eth_chainId") {
			t.Errorf("retried request lost its body: %q", body)
		}
		if calls.Add(1) <= 2 {
			io.WriteString(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32007,"message":"50/second request limit reached - reduce calls per second"}}`)
			return
		}
		io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":"0x1"}`)
	}))
	defer srv.Close()

	_, body := doJSONRPC(t, srv.URL)
	if !strings.Contains(body, `"0x1"`) {
		t.Fatalf("expected retried success result, got %q", body)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("expected 3 attempts, got %d", got)
	}
}

func TestRetryTransportRetriesHTTP429(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":"0x1"}`)
	}))
	defer srv.Close()

	resp, body := doJSONRPC(t, srv.URL)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"0x1"`) {
		t.Fatalf("expected success after 429 retry, got status %d body %q", resp.StatusCode, body)
	}
}

func TestRetryTransportGivesUpAfterMaxAttempts(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, `{"jsonrpc":"2.0","id":1,"error":{"message":"rate limit exceeded"}}`)
	}))
	defer srv.Close()

	_, body := doJSONRPC(t, srv.URL)
	if !strings.Contains(body, "rate limit") {
		t.Fatalf("expected the final rate-limit error to surface, got %q", body)
	}
	if got := calls.Load(); got != rateLimitRetries+1 {
		t.Fatalf("expected %d attempts, got %d", rateLimitRetries+1, got)
	}
}

func TestRetryTransportPassesThroughNormalErrors(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, `{"jsonrpc":"2.0","id":1,"error":{"code":3,"message":"execution reverted"}}`)
	}))
	defer srv.Close()

	_, body := doJSONRPC(t, srv.URL)
	if !strings.Contains(body, "execution reverted") {
		t.Fatalf("expected pass-through body, got %q", body)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("normal errors must not be retried; got %d attempts", got)
	}
}

func TestRetryTransportPassesThroughLargePayloads(t *testing.T) {
	large := `{"jsonrpc":"2.0","id":1,"result":"` + strings.Repeat("ab", rateLimitSniffLimit) + `"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, large)
	}))
	defer srv.Close()

	_, body := doJSONRPC(t, srv.URL)
	if body != large {
		t.Fatalf("large payload was corrupted: got %d bytes, want %d", len(body), len(large))
	}
}
