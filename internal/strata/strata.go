// Package strata is a minimal OpenAI-compatible chat client for the local
// Strata engine. It performs no logging of request bodies or keys; the API
// key is passed in by the caller (resolved at runtime from env or the
// quadlet service.env via ReadEnvValue).
package strata

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Message is an OpenAI-compatible chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Client talks to a /v1/chat/completions endpoint.
type Client struct {
	HTTP    *http.Client
	BaseURL string // e.g. http://127.0.0.1:11434/v1
	APIKey  string
	Model   string
}

// New returns a client with a sane default timeout.
func New(baseURL, apiKey, model string) *Client {
	return &Client{
		BaseURL: baseURL,
		APIKey:  apiKey,
		Model:   model,
		HTTP:    &http.Client{Timeout: 120 * time.Second},
	}
}

var (
	ErrEmptyChoices = errors.New("strata: empty choices")
	ErrKeyNotFound  = errors.New("strata: key not found in env file")
)

// maxResponseBytes caps how much of a response body we buffer.
const maxResponseBytes = 4 << 20

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature"`
	Stream      bool      `json:"stream"`
}

type chatResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
}

// Chat sends the messages and returns the first choice's content.
func (c *Client) Chat(ctx context.Context, msgs []Message, temperature float64) (string, error) {
	body, err := json.Marshal(chatRequest{
		Model:       c.Model,
		Messages:    msgs,
		Temperature: temperature,
	})
	if err != nil {
		return "", fmt.Errorf("strata: marshal: %w", err)
	}

	url := strings.TrimRight(c.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("strata: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("strata: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return "", fmt.Errorf("strata: read: %w", err)
	}
	if int64(len(data)) > maxResponseBytes {
		return "", fmt.Errorf("strata: response larger than %d bytes", maxResponseBytes)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("strata: HTTP %d: %s", resp.StatusCode, snippet(data))
	}

	var cr chatResponse
	if uerr := json.Unmarshal(data, &cr); uerr != nil {
		return "", fmt.Errorf("strata: bad json: %w", uerr)
	}
	if len(cr.Choices) == 0 {
		return "", ErrEmptyChoices
	}
	return cr.Choices[0].Message.Content, nil
}

// snippet truncates an error body so responses never end up in full in logs.
func snippet(data []byte) string {
	s := strings.TrimSpace(string(data))
	if r := []rune(s); len(r) > 200 {
		return string(r[:200]) + "…"
	}
	return s
}

// ReadEnvValue parses KEY=value lines from an env file (comments and blank
// lines skipped, one pair of surrounding double quotes stripped; for
// duplicate keys the last line wins, matching dotenv convention). Used to
// pull the API key from the quadlet service.env at runtime — never print
// the value.
func ReadEnvValue(path, key string) (string, error) {
	// #nosec G304 -- path comes from local tool config, not user input
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("strata: open env file: %w", err)
	}
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(f)
	if err != nil {
		return "", fmt.Errorf("strata: read env file: %w", err)
	}

	var found bool
	var value string
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != key {
			continue
		}
		value = strings.TrimSpace(v)
		if len(value) >= 2 && strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
			value = value[1 : len(value)-1] // strip exactly one wrapping quote pair
		}
		found = true
	}
	if !found {
		return "", fmt.Errorf("%w: %s", ErrKeyNotFound, key)
	}
	return value, nil
}
