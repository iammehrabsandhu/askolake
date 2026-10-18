package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"olake-ask/internal/domain"
	"olake-ask/internal/tuning"
)

var errEmptyCompletion = errors.New("empty completion")

// OpenAI is an OpenAI-compatible /chat/completions client (domain.LLM).
type OpenAI struct {
	c       *http.Client
	url     string
	headers map[string]string
	debug   io.Writer // non-nil: print each request body (DEBUG=on)
}

// NewOpenAI returns a client for baseURL (e.g. https://api.groq.com/openai/v1).
func NewOpenAI(baseURL, apiKey string) *OpenAI {
	return &OpenAI{
		c:       NewHTTPClient(),
		url:     strings.TrimRight(baseURL, "/") + "/chat/completions",
		headers: map[string]string{"Authorization": "Bearer " + apiKey},
	}
}

type oaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type oaRequest struct {
	Model       string      `json:"model"`
	Messages    []oaMessage `json:"messages"`
	Temperature float64     `json:"temperature"`
	MaxTokens   int         `json:"max_tokens"`
}

type oaResponse struct {
	Choices []struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// SetDebug prints every outgoing request body to w (nil disables).
func (o *OpenAI) SetDebug(w io.Writer) { o.debug = w }

// Complete implements domain.LLM.
func (o *OpenAI) Complete(ctx context.Context, req domain.LLMRequest) (domain.LLMResponse, error) {
	body := oaRequest{
		Model:       req.Model,
		Messages:    []oaMessage{{Role: "system", Content: req.System}, {Role: "user", Content: req.User}},
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}
	var resp oaResponse
	printRequest(o.debug, http.MethodPost, o.url, body)
	if _, err := DoJSON(ctx, o.c, http.MethodPost, o.url, o.headers, body, &resp, tuning.LLMRetries); err != nil {
		return domain.LLMResponse{}, fmt.Errorf("openai-compatible: %w", err)
	}
	if len(resp.Choices) == 0 {
		return domain.LLMResponse{}, fmt.Errorf("openai-compatible: %w", errEmptyCompletion)
	}
	text := strings.TrimSpace(contentText(resp.Choices[0].Message.Content))
	if text == "" {
		return domain.LLMResponse{}, fmt.Errorf("openai-compatible: %w", errEmptyCompletion)
	}
	return domain.LLMResponse{Text: text, InTokens: resp.Usage.PromptTokens, OutTokens: resp.Usage.CompletionTokens}, nil
}

// contentText accepts a string or an array of {type,text} parts.
func contentText(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}
