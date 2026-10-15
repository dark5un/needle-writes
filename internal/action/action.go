// Package action defines the built-in writing transformations: the action
// registry, prompt templates (English instructions, fabric-derived), input
// guards, and output post-filtering.
package action

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// MaxSelectionRunes bounds the selection we send to the model.
const MaxSelectionRunes = 20000

var (
	ErrUnknownAction  = errors.New("unknown action")
	ErrEmptySelection = errors.New("empty selection")
	ErrTooLarge       = errors.New("selection too large")
)

// Action is one named writing transformation.
type Action struct {
	Name         string
	Description  string
	SystemPrompt string
	Temperature  float64
}

// Message is an OpenAI-compatible chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

const onlyText = "Output ONLY the resulting text with no preamble, no explanation, no quotes or code fences around it."

var builtins = []Action{
	{
		Name:         "proofread",
		Description:  "Fix spelling, grammar and punctuation, keep the voice",
		Temperature:  0.2,
		SystemPrompt: "You are a meticulous copy editor. Correct spelling, grammar and punctuation in the user's text. Preserve the original wording, tone and meaning wherever possible. " + onlyText,
	},
	{
		Name:         "shorten",
		Description:  "Cut to roughly half the length, keep key information",
		Temperature:  0.4,
		SystemPrompt: "Rewrite the user's text to about half its length while keeping every key fact and the overall meaning. " + onlyText,
	},
	{
		Name:         "professional",
		Description:  "Rewrite in a clear professional tone",
		Temperature:  0.5,
		SystemPrompt: "Rewrite the user's text in a clear, professional tone suitable for workplace communication. Preserve the meaning and all facts. " + onlyText,
	},
	{
		Name:         "casual",
		Description:  "Rewrite in a friendly conversational tone",
		Temperature:  0.7,
		SystemPrompt: "Rewrite the user's text in a friendly, conversational tone without losing its meaning. " + onlyText,
	},
	{
		Name:         "summarize",
		Description:  "Concise bullet-point summary",
		Temperature:  0.3,
		SystemPrompt: "Summarize the user's text as concise bullet points covering the main ideas. " + onlyText,
	},
	{
		Name:         "translate-en",
		Description:  "Translate to English",
		Temperature:  0.3,
		SystemPrompt: "Translate the user's text into natural English. If it is already English, return it unchanged. " + onlyText,
	},
	{
		Name:         "translate-el",
		Description:  "Translate to Greek",
		Temperature:  0.3,
		SystemPrompt: "Translate the user's text into natural Modern Greek. " + onlyText,
	},
}

// List returns the built-in action names in order.
func List() []string {
	names := make([]string, 0, len(builtins))
	for _, a := range builtins {
		names = append(names, a.Name)
	}
	return names
}

// Get returns the action with the given name.
func Get(name string) (Action, error) {
	for _, a := range builtins {
		if a.Name == name {
			return a, nil
		}
	}
	return Action{}, fmt.Errorf("%w: %s", ErrUnknownAction, name)
}

// BuildMessages validates the selection and returns the chat messages for it.
func BuildMessages(name, text string) ([]Message, error) {
	a, err := Get(name)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(text) == "" {
		return nil, ErrEmptySelection
	}
	if r := []rune(text); len(r) > MaxSelectionRunes {
		return nil, fmt.Errorf("%w: %d runes (max %d)", ErrTooLarge, len(r), MaxSelectionRunes)
	}
	return []Message{{Role: "system", Content: a.SystemPrompt}, {Role: "user", Content: text}}, nil
}

var preambleRE = regexp.MustCompile(
	`(?i)^(?:here(?:'|’)?s|here is|here are|sure,? here (?:is|are)|below (?:is|are)|the (?:corrected|proofread|rewritten|shortened|summarized|translated|improved|resulting) (?:text|version|result|is|following))[^\n]*:?\s*\n+\s*` +
		`|^(?:certainly|of course|no problem)\s*[!?.]*:?\s*\n+\s*`)

// fenceLangTags are the fence info-strings LLMs actually emit. Membership
// is checked exactly: any single-token regex would drop real one-word
// content (a fenced "Hello" is content, not a language tag).
var fenceLangTags = map[string]bool{
	"text": true, "txt": true, "plain": true, "code": true, "output": true,
	"markdown": true, "md": true, "html": true, "css": true, "json": true,
	"yaml": true, "yml": true, "toml": true, "xml": true, "csv": true,
	"go": true, "python": true, "py": true, "js": true, "javascript": true,
	"ts": true, "typescript": true, "jsx": true, "tsx": true, "bash": true,
	"sh": true, "shell": true, "zsh": true, "sql": true, "rust": true,
	"c": true, "cpp": true, "h": true, "java": true, "kotlin": true,
	"ruby": true, "php": true, "diff": true, "mermaid": true, "dot": true,
}

// isLangTag reports whether the first line after an opening code fence is a
// fence language tag (go, python, json, ...) rather than content.
func isLangTag(s string) bool { return fenceLangTags[strings.ToLower(s)] }

// Clean strips the LLM chatter shapes we explicitly asked it not to produce,
// as a second line of defence: preamble lines, wrapping code fences, and
// wrapping double quotes.
func Clean(out string) string {
	s := strings.TrimSpace(out)
	s = preambleRE.ReplaceAllString(s, "")
	if strings.HasPrefix(s, "```") && strings.HasSuffix(s, "```") && strings.Count(s, "```") == 2 {
		rest := s[3 : len(s)-3]
		// Drop the opening fence's first line only when it is empty or a
		// known language tag (see fenceLangTags); never when it is
		// content. Revert-guard: stripping must never empty the output.
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			if tag := strings.TrimSpace(rest[:nl]); tag == "" || isLangTag(tag) {
				if stripped := rest[nl+1:]; strings.TrimSpace(stripped) != "" {
					rest = stripped
				}
			}
		}
		s = strings.TrimSpace(rest)
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' && strings.Count(s, `"`) == 2 {
		s = s[1 : len(s)-1]
	}
	return strings.TrimSpace(s)
}
