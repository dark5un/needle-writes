package strata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// okServer serves a chat completion with the given content.
func okServer(content string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"` + content + `"}}]}`))
	}))
}

func TestEnginePrefersPrimary(t *testing.T) {
	var fbHits int
	fb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fbHits++
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"from-fb"}}]}`))
	}))
	t.Cleanup(fb.Close)
	pSrv := okServer("from-primary")
	t.Cleanup(pSrv.Close)

	e := &Engine{
		Primary:  &Client{BaseURL: pSrv.URL + "/v1", Model: "p", HTTP: pSrv.Client()},
		Fallback: &Client{BaseURL: fb.URL + "/v1", Model: "f", HTTP: fb.Client()},
	}
	text, source, err := e.Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, 0.2)
	if err != nil {
		t.Fatal(err)
	}
	if text != "from-primary" || source != "primary" {
		t.Fatalf("got %q/%q, want from-primary/primary", text, source)
	}
	if fbHits != 0 {
		t.Fatalf("fallback hit %d times; primary succeeded, it must not be contacted", fbHits)
	}
}

func TestEngineFallsBackOnPrimaryFailure(t *testing.T) {
	pSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "overloaded", http.StatusInternalServerError)
	}))
	t.Cleanup(pSrv.Close)
	fbSrv := okServer("from-fallback")
	t.Cleanup(fbSrv.Close)

	e := &Engine{
		Primary:  &Client{BaseURL: pSrv.URL + "/v1", Model: "p", HTTP: pSrv.Client()},
		Fallback: &Client{BaseURL: fbSrv.URL + "/v1", Model: "f", HTTP: fbSrv.Client()},
	}
	text, source, err := e.Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, 0.2)
	if err != nil {
		t.Fatal(err)
	}
	if text != "from-fallback" || source != "fallback" {
		t.Fatalf("got %q/%q, want from-fallback/fallback", text, source)
	}
}

func TestEngineFallsBackOnEmptyResult(t *testing.T) {
	// The mac contract: the fallback runs on any primary failure,
	// including an empty model result — an empty delivery is never
	// preferable to the secondary endpoint.
	pSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"   "}}]}`))
	}))
	t.Cleanup(pSrv.Close)
	fbSrv := okServer("from-fallback")
	t.Cleanup(fbSrv.Close)

	e := &Engine{
		Primary:  &Client{BaseURL: pSrv.URL + "/v1", Model: "p", HTTP: pSrv.Client()},
		Fallback: &Client{BaseURL: fbSrv.URL + "/v1", Model: "f", HTTP: fbSrv.Client()},
	}
	text, source, err := e.Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, 0.2)
	if err != nil {
		t.Fatal(err)
	}
	if source != "fallback" || text != "from-fallback" {
		t.Fatalf("empty primary result must trigger fallback: got %q/%q", text, source)
	}
}

func TestEngineAnnouncesFallbackBeforeUse(t *testing.T) {
	// Privacy contract (mac parity): the announce hook fires when the
	// fallback is DISPATCHED, not when it succeeds — the text has left
	// the machine even if both endpoints fail. And it fires exactly once.
	var announced []string
	var fbHits int
	pSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	t.Cleanup(pSrv.Close)
	fbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fbHits++
		if len(announced) == 0 {
			t.Error("OnFallback must fire BEFORE the fallback request is served")
		}
		http.Error(w, "nope", http.StatusForbidden)
	}))
	t.Cleanup(fbSrv.Close)

	e := &Engine{
		Primary:    &Client{BaseURL: pSrv.URL + "/v1", Model: "p", HTTP: pSrv.Client()},
		Fallback:   &Client{BaseURL: fbSrv.URL + "/v1", Model: "f", HTTP: fbSrv.Client()},
		OnFallback: func(url string) { announced = append(announced, url) },
	}
	_, _, err := e.Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, 0.2)
	if err == nil {
		t.Fatal("want combined error when both fail")
	}
	if len(announced) != 1 || announced[0] != e.Fallback.BaseURL {
		t.Fatalf("announce = %v, want exactly one with the fallback URL", announced)
	}
	if fbHits != 1 {
		t.Fatalf("fallback hits = %d, want 1", fbHits)
	}
}

func TestEngineRetriesLengthFinishDoubledBudget(t *testing.T) {
	// Thinking models spend the budget on reasoning: a "length" finish
	// means the visible answer was cut off. Same endpoint, doubled
	// max_tokens, capped at 4x the configured budget (mac contract).
	var gotBudgets []float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotBudgets = append(gotBudgets, body["max_tokens"].(float64))
		if len(gotBudgets) < 2 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"cut off"},"finish_reason":"length"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"full answer"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(srv.Close)
	e := &Engine{Primary: &Client{BaseURL: srv.URL + "/v1", Model: "p", HTTP: srv.Client(), MaxTokens: 100}}
	text, source, err := e.Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, 0.2)
	if err != nil {
		t.Fatal(err)
	}
	if text != "full answer" || source != "primary" {
		t.Fatalf("got %q/%q", text, source)
	}
	if len(gotBudgets) != 2 || gotBudgets[0] != 100 || gotBudgets[1] != 200 {
		t.Fatalf("budget sequence = %v, want [100 200]", gotBudgets)
	}
}

func TestEngineLengthRetryCappedAtFourTimes(t *testing.T) {
	// The cap is a request count guard: 100 -> 200 -> 400, then the
	// last (truncated but non-empty) answer is delivered — exactly the
	// mac fallthrough, not an error.
	var gotBudgets []float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotBudgets = append(gotBudgets, body["max_tokens"].(float64))
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"truncated at ` + fmt.Sprintf("%d", len(gotBudgets)) + `"},"finish_reason":"length"}]}`))
	}))
	t.Cleanup(srv.Close)
	e := &Engine{Primary: &Client{BaseURL: srv.URL + "/v1", Model: "p", HTTP: srv.Client(), MaxTokens: 100}}
	text, _, err := e.Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, 0.2)
	if err != nil {
		t.Fatal(err)
	}
	if text != "truncated at 3" {
		t.Fatalf("text = %q, want the capped last attempt", text)
	}
	if len(gotBudgets) != 3 || gotBudgets[2] != 400 {
		t.Fatalf("budget sequence = %v, want [100 200 400]", gotBudgets)
	}
}

func TestEngineAnnouncesBudgetRetry(t *testing.T) {
	// mac parity: complete() logs "engine hit the token budget (N);
	// retrying doubled" before each doubling retry. The Go engine must
	// surface the same event through the hook — silent truncation
	// retries are an observability gap.
	var notes []string
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		if hits == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"cut"},"finish_reason":"length"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"full"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(srv.Close)
	e := &Engine{
		Primary: &Client{BaseURL: srv.URL + "/v1", Model: "p", HTTP: srv.Client(), MaxTokens: 100},
		OnBudgetRetry: func(endpoint string, nextBudget int) {
			notes = append(notes, fmt.Sprintf("%s:%d", endpoint, nextBudget))
		},
	}
	text, _, err := e.Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, 0.2)
	if err != nil {
		t.Fatal(err)
	}
	if text != "full" {
		t.Fatalf("text = %q", text)
	}
	if len(notes) != 1 || notes[0] != e.Primary.BaseURL+":100" {
		t.Fatalf("budget-retry notes = %v, want one announce of the hit budget", notes)
	}
}

func TestEngineCleanedEmptyResultTriggersFallback(t *testing.T) {
	// Emptiness is judged AFTER the output cleaner (mac checks
	// Cleaner.clean(content)): a primary reply that cleans to nothing
	// is a failure, not a delivery of blank text.
	var announced int
	pSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"chatter"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(pSrv.Close)
	fbSrv := okServer("from-fallback")
	t.Cleanup(fbSrv.Close)

	e := &Engine{
		Primary:  &Client{BaseURL: pSrv.URL + "/v1", Model: "p", HTTP: pSrv.Client()},
		Fallback: &Client{BaseURL: fbSrv.URL + "/v1", Model: "f", HTTP: fbSrv.Client()},
		Clean: func(s string) string {
			if s == "chatter" {
				return ""
			}
			return s
		},
		OnFallback: func(string) { announced++ },
	}
	text, source, err := e.Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, 0.2)
	if err != nil {
		t.Fatal(err)
	}
	if source != "fallback" || text != "from-fallback" {
		t.Fatalf("got %q/%q, want from-fallback/fallback", text, source)
	}
	if announced != 1 {
		t.Fatalf("announced = %d, want 1", announced)
	}
}

func TestEngineWithoutFallbackKeepsPrimaryError(t *testing.T) {
	pSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	t.Cleanup(pSrv.Close)
	e := &Engine{Primary: &Client{BaseURL: pSrv.URL + "/v1", Model: "p", HTTP: pSrv.Client()}}
	_, source, err := e.Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, 0.2)
	if err == nil {
		t.Fatal("want error when the only endpoint fails")
	}
	if source != "primary" {
		t.Fatalf("source = %q, want primary", source)
	}
}

func TestEngineBothFailCombinesErrors(t *testing.T) {
	// One combined message so the user sees BOTH failures — the mac
	// engine error shape ("primary: ...; fallback: ...").
	pSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	t.Cleanup(pSrv.Close)
	fbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	t.Cleanup(fbSrv.Close)

	e := &Engine{
		Primary:  &Client{BaseURL: pSrv.URL + "/v1", Model: "p", HTTP: pSrv.Client()},
		Fallback: &Client{BaseURL: fbSrv.URL + "/v1", Model: "f", HTTP: fbSrv.Client()},
	}
	_, _, err := e.Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, 0.2)
	if err == nil {
		t.Fatal("want combined error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "502") || !strings.Contains(msg, "403") {
		t.Fatalf("combined error must name both failures, got: %q", msg)
	}
	if !strings.Contains(msg, "primary") || !strings.Contains(msg, "fallback") {
		t.Fatalf("combined error must label both endpoints, got: %q", msg)
	}
}
