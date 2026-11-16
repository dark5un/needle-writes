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
	"net"
	"net/http"
	"net/url"
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
	HTTP      *http.Client
	BaseURL   string // e.g. http://127.0.0.1:11434/v1
	APIKey    string
	Model     string
	MaxTokens int // generated-token budget; 0 = omit from the request
}

// dialTimeout bounds ONLY the connect phase: an endpoint that is down,
// unroutable or behind a dead VPN must fail fast and be reported as
// unavailable, while an established connection gets the full
// (configurable) request timeout for the model to think.
const dialTimeout = 10 * time.Second

// New returns a client with a sane default timeout.
func New(baseURL, apiKey, model string) *Client {
	return &Client{
		BaseURL: baseURL,
		APIKey:  apiKey,
		Model:   model,
		HTTP: &http.Client{
			Timeout: 120 * time.Second,
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
				DialContext: (&net.Dialer{
					Timeout:   dialTimeout,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				ForceAttemptHTTP2: true,
			},
		},
	}
}

var (
	ErrEmptyChoices = errors.New("strata: empty choices")
	ErrKeyNotFound  = errors.New("strata: key not found in env file")
)

// BackendError classifies an endpoint failure with a short,
// user-presentable Reason and the offending Host, wrapping the
// underlying transport error so nothing is lost from stderr. The
// distinction matters at the UX level: "LLM backend not available"
// (down server, dead VPN, wrong port) is actionable, "timed out" is a
// slow-model signal, and neither should be mistaken for the other.
type BackendError struct {
	Err    error  // underlying failure (kept for stderr detail)
	Reason string // one-line, safe to show in a desktop notice
	Host   string // endpoint host:port from BaseURL
}

func (e *BackendError) Error() string {
	if e.Host == "" {
		return fmt.Sprintf("%s: %v", e.Reason, e.Err)
	}
	return fmt.Sprintf("%s (%s): %v", e.Reason, e.Host, e.Err)
}

// Unwrap keeps the transport error reachable via errors.Is/As.
func (e *BackendError) Unwrap() error { return e.Err }

// Notice extracts the user-presentable reason from an engine error,
// or "" when the failure is not classifiable (callers then fall back
// to their generic wording). The chain may carry the BackendError
// under extra %w wrapping (the engine's "primary: ...; fallback: ..."
// composition), which errors.As sees through.
func Notice(err error) string {
	var be *BackendError
	if errors.As(err, &be) {
		if be.Host == "" {
			return be.Reason
		}
		return be.Reason + " (" + be.Host + ")"
	}
	if errors.Is(err, ErrEmptyResult) {
		return "model returned an empty result"
	}
	return ""
}

// hostOf parses the authority out of a BaseURL for notice wording.
func hostOf(baseURL string) string {
	if u, err := url.Parse(baseURL); err == nil && u.Host != "" {
		return u.Host
	}
	return baseURL
}

// classifyTransport maps a failed HTTP round trip to a BackendError:
// dial-phase failures (refused, no route, unresolved name, connect
// timeout) mean the endpoint is not reachable; a timeout after the
// connection exists means the backend is reachable but too slow.
func classifyTransport(baseURL string, err error) error {
	host := hostOf(baseURL)
	var opErr *net.OpError
	if errors.As(err, &opErr) && (opErr.Op == "dial" || opErr.Op == "connect") {
		return &BackendError{Reason: "LLM backend not available", Host: host, Err: err}
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return &BackendError{Reason: "LLM backend not available (name does not resolve)", Host: host, Err: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &BackendError{Reason: "LLM backend timed out", Host: host, Err: err}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &BackendError{Reason: "LLM backend timed out", Host: host, Err: err}
	}
	return err
}

// maxResponseBytes caps how much of a response body we buffer.
const maxResponseBytes = 4 << 20

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature"`
	Stream      bool      `json:"stream"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
}

// Chat sends the messages and returns the first choice's content, using
// the client's configured token budget.
func (c *Client) Chat(ctx context.Context, msgs []Message, temperature float64) (string, error) {
	content, _, err := c.chatAttempt(ctx, msgs, temperature, c.MaxTokens)
	return content, err
}

// chatAttempt is one completion request with an explicit token budget
// (0 = omit max_tokens). It also reports the finish_reason, which the
// Engine needs to spot budget-truncated answers.
func (c *Client) chatAttempt(ctx context.Context, msgs []Message, temperature float64, maxTokens int) (content, finishReason string, err error) {
	body, err := json.Marshal(chatRequest{
		Model:       c.Model,
		Messages:    msgs,
		Temperature: temperature,
		MaxTokens:   maxTokens,
	})
	if err != nil {
		return "", "", fmt.Errorf("strata: marshal: %w", err)
	}

	url := strings.TrimRight(c.BaseURL, "/") + "/chat/completions"
	// #nosec G704 -- the endpoint is the operator's own config/env value
	// (a local engine URL by design); there is no untrusted input here.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", "", fmt.Errorf("strata: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	// #nosec G704 -- same operator-configured endpoint as above.
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("strata: %w", classifyTransport(c.BaseURL, err))
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return "", "", fmt.Errorf("strata: read: %w", err)
	}
	if int64(len(data)) > maxResponseBytes {
		return "", "", fmt.Errorf("strata: response larger than %d bytes", maxResponseBytes)
	}
	if resp.StatusCode != http.StatusOK {
		// An error status is a known backend failure mode too: the
		// notice says so (with the code), the snippet stays on stderr.
		return "", "", &BackendError{
			Reason: fmt.Sprintf("LLM backend error: HTTP %d", resp.StatusCode),
			Host:   hostOf(c.BaseURL),
			Err:    errors.New(snippet(data)),
		}
	}

	var cr chatResponse
	if uerr := json.Unmarshal(data, &cr); uerr != nil {
		return "", "", fmt.Errorf("strata: bad json: %w", uerr)
	}
	if len(cr.Choices) == 0 {
		return "", "", ErrEmptyChoices
	}
	return cr.Choices[0].Message.Content, cr.Choices[0].FinishReason, nil
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
