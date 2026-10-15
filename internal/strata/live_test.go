//go:build live

// Smoke test against the real local Strata engine. Run with:
//
//	go test -tags live ./internal/strata/
//
// Requires the strata quadlet service up on 127.0.0.1:11434 and the key in
// the ai-lab-quadlets service.env. Never prints the key.
package strata

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLiveStrataProofread(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(home, ".config/containers/config/strata/service.env")
	key, err := ReadEnvValue(envPath, "API_KEY")
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	if len(key) < 8 {
		t.Fatal("suspiciously short key read")
	}

	c := New("http://127.0.0.1:11434/v1", key, "qwen3.8-flash-next-iq3_s")
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
