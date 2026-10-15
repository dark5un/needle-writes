package strata

import (
	"context"
	"encoding/json"
	"errors"
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
