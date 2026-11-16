package strata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &Client{
		BaseURL: srv.URL + "/v1",
		APIKey:  "test-key-not-secret",
		Model:   "qwen3.8-flash-next-iq3_s",
		HTTP:    srv.Client(),
	}, srv
}

func TestChatSendsOpenAICompliantRequest(t *testing.T) {
	var gotBody map[string]any
	var gotAuth string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Errorf("bad json from client: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"fixed text"}}]}`))
	})

	got, err := c.Chat(context.Background(),
		[]Message{{Role: "system", Content: "be good"}, {Role: "user", Content: "text"}}, 0.2)
	if err != nil {
		t.Fatal(err)
	}
	if got != "fixed text" {
		t.Fatalf("content = %q, want %q", got, "fixed text")
	}
	if gotAuth != "Bearer test-key-not-secret" {
		t.Fatalf("auth header = %q", gotAuth)
	}
	if gotBody["model"] != "qwen3.8-flash-next-iq3_s" {
		t.Fatalf("model = %v", gotBody["model"])
	}
	if gotBody["temperature"] != 0.2 {
		t.Fatalf("temperature = %v", gotBody["temperature"])
	}
	msgs, _ := gotBody["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages len = %d, want 2", len(msgs))
	}
}

func TestChatNon200IsError(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "overloaded", http.StatusTooManyRequests)
	})
	_, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, 0.2)
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("err = %v, want status 429 in message", err)
	}
}

func TestChatEmptyChoicesIsError(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[]}`))
	})
	_, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, 0.2)
	if err == nil {
		t.Fatal("want error on empty choices")
	}
}

func TestChatMalformedJSONIsError(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	})
	_, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, 0.2)
	if err == nil {
		t.Fatal("want error on malformed json")
	}
}

func TestChatRespectsContextDeadline(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := c.Chat(ctx, []Message{{Role: "user", Content: "x"}}, 0.2)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}

func TestChatSendsMaxTokensWhenSet(t *testing.T) {
	var gotBody map[string]any
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Errorf("bad json from client: %v", err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"x"}}]}`))
	})
	c.MaxTokens = 4096
	if _, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, 0.2); err != nil {
		t.Fatal(err)
	}
	if gotBody["max_tokens"] != float64(4096) {
		t.Fatalf("max_tokens = %v, want 4096", gotBody["max_tokens"])
	}
}

func TestChatOmitsMaxTokensWhenUnset(t *testing.T) {
	// Zero means "no opinion": the request must not carry the key at
	// all, so engines pick their own context budget (v0 behaviour).
	var gotBody map[string]any
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"x"}}]}`))
	})
	if _, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, 0.2); err != nil {
		t.Fatal(err)
	}
	if _, ok := gotBody["max_tokens"]; ok {
		t.Fatalf("max_tokens must be absent when unset: %v", gotBody["max_tokens"])
	}
}

func TestNewCarriesDefaultTimeout(t *testing.T) {
	c := New("http://127.0.0.1:1/v1", "", "m")
	if c.HTTP.Timeout != 120*time.Second {
		t.Fatalf("default client timeout = %v, want 120s (mac parity)", c.HTTP.Timeout)
	}
}

func TestReadEnvValueFromServiceEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "service.env")
	content := "# comment\nOTHER=x\nAPI_KEY=super-secret-value\nQUOTED=\"with spaces\"\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := ReadEnvValue(path, "API_KEY")
	if err != nil {
		t.Fatal(err)
	}
	if v != "super-secret-value" {
		t.Fatalf("API_KEY = %q", v)
	}
	q, err := ReadEnvValue(path, "QUOTED")
	if err != nil {
		t.Fatal(err)
	}
	if q != "with spaces" {
		t.Fatalf("QUOTED = %q, want quotes stripped", q)
	}
}

func TestReadEnvValueMissingKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "e.env")
	if err := os.WriteFile(path, []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ReadEnvValue(path, "API_KEY")
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("err = %v, want ErrKeyNotFound", err)
	}
}

// --- backend failure classification (the "errors should be known" contract:
// an unreachable engine must be distinguishable from a slow one, and the
// user-facing reason must be extractable from the error chain) ---

func TestConnectionRefusedIsBackendUnavailable(t *testing.T) {
	// Closed server: dial gets connection refused immediately.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	c := New(url+"/v1", "test-key-not-secret", "m")
	_, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, 0)
	var be *BackendError
	if !errors.As(err, &be) {
		t.Fatalf("err = %v, want a *BackendError", err)
	}
	if be.Reason != "LLM backend not available" {
		t.Fatalf("reason = %q", be.Reason)
	}
	if be.Host == "" || !strings.Contains(err.Error(), be.Host) {
		t.Fatalf("err = %v, must name the endpoint host", err)
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("err = %v, must keep the underlying transport detail", err)
	}
}

func TestUnresolvableHostIsBackendUnavailable(t *testing.T) {
	c := New("http://needle.invalid:11434/v1", "test-key-not-secret", "m")
	_, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, 0)
	var be *BackendError
	if !errors.As(err, &be) || be.Reason != "LLM backend not available" {
		t.Fatalf("err = %v, want BackendUnavailable classification", err)
	}
}

func TestRequestTimeoutIsClassifiedSeparately(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = io.WriteString(w, `{}`)
	})
	c.HTTP.Timeout = 50 * time.Millisecond
	_, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, 0)
	var be *BackendError
	if !errors.As(err, &be) {
		t.Fatalf("err = %v, want a *BackendError", err)
	}
	if be.Reason != "LLM backend timed out" {
		t.Fatalf("reason = %q, want the slow-backend class", be.Reason)
	}
}

func TestHTTPErrorStatusIsClassified(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model is loading", http.StatusServiceUnavailable)
	})
	_, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, 0)
	var be *BackendError
	if !errors.As(err, &be) {
		t.Fatalf("err = %v, want a *BackendError", err)
	}
	if be.Reason != "LLM backend error: HTTP 503" {
		t.Fatalf("reason = %q", be.Reason)
	}
	if !strings.Contains(err.Error(), "model is loading") {
		t.Fatalf("err = %v, must keep the response snippet", err)
	}
}

func TestNewClientDialsFast(t *testing.T) {
	// A blackholed connect (VPN down, SYN dropped) must fail within the
	// dial budget, not the full per-request timeout.
	c := New("http://10.255.255.1:11434/v1", "test-key-not-secret", "m")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := c.Chat(ctx, []Message{{Role: "user", Content: "x"}}, 0)
	var be *BackendError
	if !errors.As(err, &be) || be.Reason != "LLM backend not available" {
		t.Fatalf("err = %v, want unavailable within the dial budget", err)
	}
}

func TestNoticeExtractsUserFacingReason(t *testing.T) {
	be := &BackendError{Reason: "LLM backend not available", Host: "ai.example:11434", Err: errors.New("dial: no route")}
	if got := Notice(fmt.Errorf("strata: %w", be)); got != "LLM backend not available (ai.example:11434)" {
		t.Fatalf("Notice = %q", got)
	}
	if got := Notice(ErrEmptyResult); got != "model returned an empty result" {
		t.Fatalf("Notice(empty) = %q", got)
	}
	if got := Notice(errors.New("boom")); got != "" {
		t.Fatalf("Notice(unknown) = %q, want empty", got)
	}
}
