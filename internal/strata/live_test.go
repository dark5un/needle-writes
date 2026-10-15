//go:build live

// Smoke test against the real local engine, resolved through the same
// config layer the binary uses (env > config file). Run with:
//
//	go test -tags live ./internal/strata/
//
// Requires an OpenAI-compatible engine up and a key configured.
// Never prints the key.
package strata

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dark5un/needle-writes/internal/config"
)

func TestLiveStrataProofread(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	key := os.Getenv("NEEDLE_API_KEY")
	if key == "" && cfg.KeyFile != "" {
		key, err = ReadEnvValue(cfg.KeyFile, cfg.KeyVar)
		if err != nil {
			t.Fatalf("read key: %v", err)
		}
	}
	if len(key) < 8 {
		t.Fatal("suspiciously short key read")
	}
	endpoint := os.Getenv("NEEDLE_STRATA_URL")
	if endpoint == "" {
		endpoint = cfg.Endpoint
	}
	model := os.Getenv("NEEDLE_MODEL")
	if model == "" {
		model = cfg.Model
	}

	c := New(endpoint, key, model)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	out, err := c.Chat(ctx, []Message{
		{Role: "system", Content: "Correct spelling and grammar only. Output ONLY the corrected text."},
		//nolint:misspell // "teh" is an intentional typo in the proofread fixture
		{Role: "user", Content: "She dont has a apple, and teh cat are sleeping good."},
	}, 0.2)
	if err != nil {
		t.Fatalf("live chat: %v", err)
	}
	t.Logf("model output: %q", strings.TrimSpace(out))
	low := strings.ToLower(out)
	for _, want := range []string{"doesn't", "an apple", "the cat"} {
		if !strings.Contains(low, want) {
			t.Errorf("output missing %q: %q", want, out)
		}
	}
}
