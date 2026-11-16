// Engine routes a chat call through the primary (local) endpoint and,
// only when that fails, an optional operator-configured fallback —
// mirroring the needle-mac engine contract (fallback on transport
// failure, HTTP error, or an empty result). Fallback use is reported
// to the caller via the returned source so the one-shot can announce
// that the text left the machine.
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

// ErrEmptyResult is what an endpoint returning blank content maps to:
// an empty rewrite is a failure, never a delivery.
var ErrEmptyResult = errors.New("empty result")

// Engine is a primary client plus an optional fallback (nil = local-only,
// the shipped default).
type Engine struct {
	Primary  *Client
	Fallback *Client
}

// chat runs one client and treats blank content as a failure.
func chat(ctx context.Context, c *Client, msgs []Message, temperature float64) (string, error) {
	text, err := c.Chat(ctx, msgs, temperature)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return "", ErrEmptyResult
	}
	return text, nil
}

// Chat returns the first non-empty result and the source that served
// it. When both endpoints fail the error names both, mac-style:
// "primary: ...; fallback: ...".
func (e *Engine) Chat(ctx context.Context, msgs []Message, temperature float64) (text, source string, err error) {
	text, err = chat(ctx, e.Primary, msgs, temperature)
	if err == nil {
		return text, SourcePrimary, nil
	}
	primaryErr := err
	if e.Fallback == nil {
		return "", SourcePrimary, primaryErr
	}
	fbText, fbErr := chat(ctx, e.Fallback, msgs, temperature)
	if fbErr == nil {
		return fbText, SourceFallback, nil
	}
	return "", SourceFallback, fmt.Errorf("primary: %w; fallback: %w", primaryErr, fbErr)
}
