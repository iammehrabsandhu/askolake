package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"olake-ask/internal/domain"
	"olake-ask/internal/tuning"
)

// Anthropic is a /v1/messages client (domain.LLM).
type Anthropic struct {
	c       *http.Client
	url     string
	headers map[string]string
	debug   io.Writer // non-nil: print each request body (DEBUG=on)
}

// NewAnthropic returns a client for baseURL (default https://api.anthropic.com).
func NewAnthropic(baseURL, apiKey string) *Anthropic {
	return &Anthropic{
		c:   NewHTTPClient(),
		url: strings.TrimRight(baseURL, "/") + "/v1/messages",
		headers: map[string]string{
			"x-api-key":         apiKey,
			"anthropic-version": "2023-06-01",
		},
	}
}

type anMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anRequest struct {
	Model       string      `json:"model"`
	MaxTokens   int         `json:"max_tokens"`
	Temperature float64     `json:"temperature"`
	System      string      `json:"system"`
	Messages    []anMessage `json:"messages"`
}

type anResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// SetDebug prints every outgoing request body to w (nil disables).
func (a *Anthropic) SetDebug(w io.Writer) { a.debug = w }

// Complete implements domain.LLM.
func (a *Anthropic) Complete(ctx context.Context, req domain.LLMRequest) (domain.LLMResponse, error) {
	body := anRequest{
		Model:       req.Model,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		System:      req.System,
		Messages:    []anMessage{{Role: "user", Content: req.User}},
	}
	var resp anResponse
	printRequest(a.debug, http.MethodPost, a.url, body)
	if _, err := DoJSON(ctx, a.c, http.MethodPost, a.url, a.headers, body, &resp, tuning.LLMRetries); err != nil {
		return domain.LLMResponse{}, fmt.Errorf("anthropic: %w", err)
	}
	var b strings.Builder
	for _, c := range resp.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	text := strings.TrimSpace(b.String())
	if text == "" {
		return domain.LLMResponse{}, fmt.Errorf("anthropic: %w", errEmptyCompletion)
	}
	return domain.LLMResponse{Text: text, InTokens: resp.Usage.InputTokens, OutTokens: resp.Usage.OutputTokens}, nil
}
