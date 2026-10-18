package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"

	"olake-ask/internal/domain"
	"olake-ask/internal/tuning"
)

var errEmptyCompletion = errors.New("empty completion")

// OpenAI is an OpenAI-compatible /chat/completions client (domain.LLM). It
// speaks OpenRouter's extensions too: a `models` fallback list and
// `reasoning` effort.
type OpenAI struct {
	c         *http.Client
	url       string
	headers   map[string]string
	debug     io.Writer // non-nil: print each request body (DEBUG=on)
	fallbacks []string  // OpenRouter `models`: tried in order if req.Model fails
	effort    string    // OpenRouter `reasoning.effort`; "" = omit
	// noEffort is set once a request succeeded only without `reasoning`, so
	// later requests stop sending it.
	noEffort atomic.Bool
}

// NewOpenAI returns a client for baseURL (e.g. https://openrouter.ai/api/v1).
func NewOpenAI(baseURL, apiKey string) *OpenAI {
	return &OpenAI{
		c:       NewHTTPClient(),
		url:     strings.TrimRight(baseURL, "/") + "/chat/completions",
		headers: map[string]string{"Authorization": "Bearer " + strings.TrimSpace(apiKey)},
	}
}

type oaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type oaReasoning struct {
	Effort string `json:"effort"`
}

type oaRequest struct {
	Model       string       `json:"model"`
	Models      []string     `json:"models,omitempty"`
	Messages    []oaMessage  `json:"messages"`
	Temperature float64      `json:"temperature"`
	MaxTokens   int          `json:"max_tokens"`
	Reasoning   *oaReasoning `json:"reasoning,omitempty"`
}

type oaResponse struct {
	Model   string `json:"model"` // the model that actually answered (after fallbacks)
	Choices []struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Code    any    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// ListModels returns the model IDs OpenRouter lists for this key
// (GET <base>/models/user); OpenRouter filters them by the key's restrictions.
func (o *OpenAI) ListModels(ctx context.Context) ([]string, error) {
	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	u := strings.TrimSuffix(o.url, "/chat/completions") + "/models/user"
	if _, err := DoJSON(ctx, o.c, http.MethodGet, u, o.headers, nil, &resp, 0); err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	ids := make([]string, 0, len(resp.Data))
	for _, m := range resp.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// SetDebug prints every outgoing request body to w (nil disables).
func (o *OpenAI) SetDebug(w io.Writer) { o.debug = w }

// SetFallbackModels sends models as OpenRouter's `models` list: if the
// primary model errors, OpenRouter tries these in order within the same call.
func (o *OpenAI) SetFallbackModels(models []string) { o.fallbacks = models }

// SetReasoningEffort sends `reasoning: {effort}` (e.g. "none" for faster,
// cheaper answers). If a model rejects it, the request is retried without it.
func (o *OpenAI) SetReasoningEffort(effort string) { o.effort = effort }

// Complete implements domain.LLM.
func (o *OpenAI) Complete(ctx context.Context, req domain.LLMRequest) (domain.LLMResponse, error) {
	body := oaRequest{
		Model:       req.Model,
		Models:      o.fallbacks,
		Messages:    []oaMessage{{Role: "system", Content: req.System}, {Role: "user", Content: req.User}},
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}
	if o.effort != "" && !o.noEffort.Load() {
		body.Reasoning = &oaReasoning{Effort: o.effort}
	}
	resp, err := o.post(ctx, body)
	var he *HTTPError
	if body.Reasoning != nil && errors.As(err, &he) && (he.Status == http.StatusBadRequest || he.Status == http.StatusUnprocessableEntity) {
		body.Reasoning = nil
		if resp, err = o.post(ctx, body); err == nil {
			o.noEffort.Store(true)
		}
	}
	if err != nil {
		return domain.LLMResponse{}, fmt.Errorf("openai-compatible: %w", withHint(err))
	}
	if resp.Error != nil && len(resp.Choices) == 0 {
		return domain.LLMResponse{}, fmt.Errorf("openai-compatible: provider error %v: %s", resp.Error.Code, resp.Error.Message)
	}
	if len(resp.Choices) == 0 {
		return domain.LLMResponse{}, fmt.Errorf("openai-compatible: %w", errEmptyCompletion)
	}
	text := strings.TrimSpace(contentText(resp.Choices[0].Message.Content))
	if text == "" {
		return domain.LLMResponse{}, fmt.Errorf("openai-compatible: %w", errEmptyCompletion)
	}
	if o.debug != nil && resp.Model != "" {
		fmt.Fprintf(o.debug, "LLM ANSWERED BY  %s\n", resp.Model)
	}
	return domain.LLMResponse{Text: text, InTokens: resp.Usage.PromptTokens, OutTokens: resp.Usage.CompletionTokens}, nil
}

func (o *OpenAI) post(ctx context.Context, body oaRequest) (oaResponse, error) {
	var resp oaResponse
	printRequest(o.debug, http.MethodPost, o.url, body)
	_, err := DoJSON(ctx, o.c, http.MethodPost, o.url, o.headers, body, &resp, tuning.LLMRetries)
	return resp, err
}

// withHint adds what to do for the statuses OpenRouter documents as
// non-retryable (429 and 5xx are already retried by DoJSON).
func withHint(err error) error {
	var he *HTTPError
	if !errors.As(err, &he) {
		return err
	}
	var hint string
	switch he.Status {
	case http.StatusUnauthorized:
		hint = "API key missing, wrong or disabled: check LLM_API_KEY"
	case http.StatusPaymentRequired:
		hint = "not enough credit left on the API key"
	case http.StatusForbidden:
		hint = "model not allowed on this key (check LLM_MODEL / LLM_FALLBACK_MODELS spelling) or credit limit used up"
	default:
		return err
	}
	return fmt.Errorf("%w (%s)", err, hint)
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
