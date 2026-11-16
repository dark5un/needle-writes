// Engine routes a chat call through the primary (local) endpoint and,
// only when that fails, an optional operator-configured fallback —
// mirroring the needle-mac engine contract: the fallback is tried on
// transport failure, HTTP error, or an empty (post-clean) result; the
// privacy announce fires BEFORE the text leaves; and a "length"
// finish_reason doubles the token budget on the SAME endpoint (up to
// 4x the configured value) because thinking models spend budget on
// reasoning before answering — a visible answer cut off mid-stream is
// a budget failure, not a result.
package strata

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Sources for the endpoint that served a successful Chat.
const (
	SourcePrimary  = "primary"
	SourceFallback = "fallback"
)

// ErrEmptyResult is what an endpoint returning blank (post-clean)
// content maps to: an empty rewrite is a failure, never a delivery.
var ErrEmptyResult = errors.New("empty result")

// lengthRetryFactor caps the per-endpoint budget doubling: attempts
// run at 1x, 2x and 4x the configured max_tokens (mac contract: retry
// while budget < 4x configured, so at most one retry beyond 4x is
// never requested).
const lengthRetryFactor = 4

// Engine is a primary client plus an optional fallback (nil = local-only,
// the shipped default).
type Engine struct {
	Primary  *Client
	Fallback *Client
	// Clean post-filters model chatter before emptiness is judged;
	// nil = identity. The delivered text is the cleaned text.
	Clean func(string) string
	// OnFallback is called immediately before every fallback attempt
	// (the selection is about to leave the machine) — never after, so
	// even a failed fallback is announced. nil = no announce.
	OnFallback func(endpointURL string)
	// OnBudgetRetry is called before each doubled-budget retry, with
	// the endpoint and the budget that was hit (mac logs the same
	// event: "engine hit the token budget (N); retrying doubled").
	OnBudgetRetry func(endpointURL string, hitBudget int)
}

func (e *Engine) clean(s string) string {
	if e.Clean != nil {
		return e.Clean(s)
	}
	return s
}

// attempt runs one endpoint's sequence: request, budget-doubling
// retries on "length" finishes, then the emptiness check on cleaned
// content. Returns the cleaned text.
func (e *Engine) attempt(ctx context.Context, c *Client, msgs []Message, temperature float64) (string, error) {
	budget := c.MaxTokens
	for {
		content, reason, err := c.chatAttempt(ctx, msgs, temperature, budget)
		if err != nil {
			return "", err
		}
		if reason == "length" && budget > 0 && budget < c.MaxTokens*lengthRetryFactor {
			if e.OnBudgetRetry != nil {
				e.OnBudgetRetry(c.BaseURL, budget) // the budget that was hit (mac wording)
			}
			budget *= 2
			continue
		}
		out := e.clean(content)
		if strings.TrimSpace(out) == "" {
			return "", ErrEmptyResult
		}
		return out, nil
	}
}

// Chat returns the first successful (cleaned, non-empty) result and the
// source that served it. When both endpoints fail the error names both,
// mac-style: "primary: ...; fallback: ...".
func (e *Engine) Chat(ctx context.Context, msgs []Message, temperature float64) (text, source string, err error) {
	text, err = e.attempt(ctx, e.Primary, msgs, temperature)
	if err == nil {
		return text, SourcePrimary, nil
	}
	primaryErr := err
	if e.Fallback == nil {
		return "", SourcePrimary, primaryErr
	}
	if e.OnFallback != nil {
		e.OnFallback(e.Fallback.BaseURL)
	}
	fbText, fbErr := e.attempt(ctx, e.Fallback, msgs, temperature)
	if fbErr == nil {
		return fbText, SourceFallback, nil
	}
	return "", SourceFallback, fmt.Errorf("primary: %w; fallback: %w", primaryErr, fbErr)
}
