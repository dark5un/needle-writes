package strata

import (
	"context"
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
