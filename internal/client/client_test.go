package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"olake-ask/internal/domain"
	"olake-ask/internal/tuning"
)

var req = domain.LLMRequest{Model: "m", System: "sys", User: "usr", MaxTokens: 10, Temperature: 0.1}

func TestOpenAISuccessAndUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("path=%s auth=%s", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		msgs := body["messages"].([]any)
		if body["model"] != "m" || len(msgs) != 2 || body["max_tokens"].(float64) != 10 {
			t.Errorf("body = %v", body)
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"  hello  "}}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`)
	}))
	defer srv.Close()
	resp, err := NewOpenAI(srv.URL+"/v1/", "k").Complete(context.Background(), req)
	if err != nil || resp.Text != "hello" || resp.InTokens != 5 || resp.OutTokens != 2 {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
}

func TestOpenAIContentParts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}}]}`)
	}))
	defer srv.Close()
	resp, err := NewOpenAI(srv.URL, "k").Complete(context.Background(), req)
	if err != nil || resp.Text != "ab" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
}

func TestOpenAIRetryOn429(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()
	resp, err := NewOpenAI(srv.URL, "k").Complete(context.Background(), req)
	if err != nil || resp.Text != "ok" || calls.Load() != 2 {
		t.Fatalf("resp=%+v err=%v calls=%d", resp, err, calls.Load())
	}
}

func TestOpenAI500Exhausted(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "0")
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	_, err := NewOpenAI(srv.URL, "k").Complete(context.Background(), req)
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != 500 || int(calls.Load()) != tuning.LLMRetries+1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestOpenAIEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"   "}}]}`)
	}))
	defer srv.Close()
	if _, err := NewOpenAI(srv.URL, "k").Complete(context.Background(), req); !errors.Is(err, errEmptyCompletion) {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenAIOpenRouterFields(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		io.WriteString(w, `{"model":"b","choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()
	o := NewOpenAI(srv.URL, "k")
	if _, err := o.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["models"]; ok {
		t.Fatalf("models sent without fallbacks: %v", body)
	}
	if _, ok := body["reasoning"]; ok {
		t.Fatalf("reasoning sent without effort: %v", body)
	}
	o.SetFallbackModels([]string{"b", "c"})
	o.SetReasoningEffort("none")
	if _, err := o.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	models, _ := body["models"].([]any)
	reasoning, _ := body["reasoning"].(map[string]any)
	if body["model"] != "m" || len(models) != 2 || models[0] != "b" || reasoning["effort"] != "none" {
		t.Fatalf("body = %v", body)
	}
}

func TestOpenAIDropsRejectedReasoning(t *testing.T) {
	var withReasoning, without atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["reasoning"]; ok {
			withReasoning.Add(1)
			http.Error(w, `{"error":{"message":"reasoning not supported"}}`, http.StatusBadRequest)
			return
		}
		without.Add(1)
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()
	o := NewOpenAI(srv.URL, "k")
	o.SetReasoningEffort("none")
	for i := 0; i < 2; i++ {
		if resp, err := o.Complete(context.Background(), req); err != nil || resp.Text != "ok" {
			t.Fatalf("resp=%+v err=%v", resp, err)
		}
	}
	if withReasoning.Load() != 1 || without.Load() != 2 {
		t.Fatalf("with=%d without=%d, want 1 and 2 (field dropped after first rejection)", withReasoning.Load(), without.Load())
	}
}

func TestOpenAIErrorHints(t *testing.T) {
	for status, want := range map[int]string{401: "LLM_API_KEY", 402: "credit", 403: "model not allowed"} {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			http.Error(w, "no", status)
		}))
		_, err := NewOpenAI(srv.URL, "k").Complete(context.Background(), req)
		srv.Close()
		var he *HTTPError
		if !errors.As(err, &he) || he.Status != status || !strings.Contains(err.Error(), want) || calls.Load() != 1 {
			t.Errorf("%d: err=%v calls=%d", status, err, calls.Load())
		}
	}
}

func TestOpenAIBodyError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"error":{"code":502,"message":"provider down"}}`)
	}))
	defer srv.Close()
	if _, err := NewOpenAI(srv.URL, "k").Complete(context.Background(), req); err == nil || !strings.Contains(err.Error(), "provider down") {
		t.Fatalf("err = %v", err)
	}
}

func TestAnthropic(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "k" || r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("path=%s headers=%v", r.URL.Path, r.Header)
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["system"] != "sys" {
			t.Errorf("system = %v", body["system"])
		}
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		io.WriteString(w, `{"content":[{"type":"text","text":"x"},{"type":"tool_use"},{"type":"text","text":"y"}],"usage":{"input_tokens":7,"output_tokens":3}}`)
	}))
	defer srv.Close()
	resp, err := NewAnthropic(srv.URL, "k").Complete(context.Background(), req)
	if err != nil || resp.Text != "xy" || resp.InTokens != 7 || resp.OutTokens != 3 {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	srv500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv500.Close()
	if _, err := NewAnthropic(srv500.URL, "k").Complete(context.Background(), req); err == nil {
		t.Fatal("expected error")
	}
}

func TestEmbeddingAPIBatching(t *testing.T) {
	var batches []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		batches = append(batches, len(body.Input))
		type item struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		var data []item
		for i := len(body.Input) - 1; i >= 0; i-- { // reversed: client must reorder by index
			data = append(data, item{Index: i, Embedding: []float32{3, float32(len(body.Input[i]))}})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()
	e := NewEmbeddingAPI(srv.URL, "k", "emb", 0)
	texts := make([]string, tuning.EmbedAPIBatch+6)
	for i := range texts {
		texts[i] = strings.Repeat("x", 4)
	}
	texts[0] = ""
	out, err := e.Embed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 2 || batches[0] != tuning.EmbedAPIBatch || batches[1] != 6 || len(out) != len(texts) {
		t.Fatalf("batches=%v out=%d", batches, len(out))
	}
	if out[0][0] != 1 || out[0][1] != 0 || out[1][0] != 0.6 || out[1][1] != 0.8 {
		t.Fatalf("not normalized/ordered: %v %v", out[0], out[1])
	}
	if info := e.Info(); info.Dim != 2 || info.Provider != "api" || info.Model != "emb" {
		t.Fatalf("info = %+v", info)
	}
}

func TestGitHubPagination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "42")
		if strings.Contains(r.URL.Path, "/comments") {
			io.WriteString(w, `[{"body":"fixed","author_association":"MEMBER"}]`)
			return
		}
		switch r.URL.Query().Get("page") {
		case "1":
			io.WriteString(w, `[{"number":1,"comments":1},{"number":2,"pull_request":{}}]`)
		case "2":
			http.Error(w, `{"message":"Pagination with the page parameter is not supported"}`, http.StatusUnprocessableEntity)
		}
	}))
	defer srv.Close()
	g := NewGitHub("").WithBase(srv.URL)
	issues, err := g.ListClosedIssues(context.Background(), "o/r", 10)
	if err != nil || len(issues) != 1 || issues[0].Number != 1 || g.Remaining() != 42 {
		t.Fatalf("issues=%+v err=%v remaining=%d", issues, err, g.Remaining())
	}
	cs, err := g.ListComments(context.Background(), "o/r", 1)
	if err != nil || len(cs) != 1 || cs[0].AuthorAssociation != "MEMBER" {
		t.Fatalf("comments=%+v err=%v", cs, err)
	}
}

func TestDebugPrintsExactRequestWithoutKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}],"content":[{"type":"text","text":"ok"}]}`)
	}))
	defer srv.Close()
	var buf strings.Builder
	o := NewOpenAI(srv.URL, "secret-key-123")
	o.SetDebug(&buf)
	if _, err := o.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	a := NewAnthropic(srv.URL, "secret-key-123")
	a.SetDebug(&buf)
	if _, err := a.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"LLM REQUEST  POST " + srv.URL + "/chat/completions", "LLM REQUEST  POST " + srv.URL + "/v1/messages", "\n  \"model\": \"m\",", "\"content\": \"usr\""} {
		if !strings.Contains(out, want) {
			t.Fatalf("debug output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "secret-key-123") {
		t.Fatal("API key printed")
	}
}

func TestCohereEmbedRequestBatchesAndNormalizes(t *testing.T) {
	var reqs []map[string]any
	var auth, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, path = r.Header.Get("Authorization"), r.URL.Path
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		reqs = append(reqs, body)
		n := len(body["texts"].([]any))
		vecs := make([][]float32, n)
		for i := range vecs {
			vecs[i] = []float32{3, 4}
		}
		json.NewEncoder(w).Encode(map[string]any{"embeddings": map[string]any{"float": vecs}})
	}))
	defer srv.Close()
	e := NewCohereEmbed(srv.URL+"/v2/", "k", "embed-v4.0", CohereSearchDocument, 0, 0)
	texts := make([]string, tuning.CohereEmbedBatch+4)
	out, err := e.Embed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 2 || len(reqs[0]["texts"].([]any)) != tuning.CohereEmbedBatch || len(out) != len(texts) {
		t.Fatalf("batches: %d requests, out %d", len(reqs), len(out))
	}
	r := reqs[0]
	if path != "/v2/embed" || auth != "Bearer k" || r["model"] != "embed-v4.0" || r["input_type"] != "search_document" || r["truncate"] != "END" {
		t.Fatalf("request: path=%s auth=%s body=%v", path, auth, r)
	}
	if types, _ := r["embedding_types"].([]any); len(types) != 1 || types[0] != "float" {
		t.Fatalf("embedding_types = %v", r["embedding_types"])
	}
	if _, ok := r["output_dimension"]; ok {
		t.Fatal("output_dimension must be omitted when 0")
	}
	if out[0][0] != 0.6 || out[0][1] != 0.8 {
		t.Fatalf("not normalized: %v", out[0])
	}
	if info := e.Info(); info.Provider != "cohere" || info.Model != "embed-v4.0" || info.Dim != 2 || info.OutputDim != 0 {
		t.Fatalf("info = %+v", info)
	}

	// Query side: output_dimension is sent, and a wrong dimension is an error.
	q := NewCohereEmbed(srv.URL+"/v2", "k", "embed-v4.0", CohereSearchQuery, 512, 512)
	if _, err := q.Embed(context.Background(), []string{"q"}); err == nil || !strings.Contains(err.Error(), "dim") {
		t.Fatalf("dim mismatch err = %v", err)
	}
	last := reqs[len(reqs)-1]
	if last["input_type"] != "search_query" || last["output_dimension"] != float64(512) {
		t.Fatalf("query request: %v", last)
	}
}

func TestCohereEmbedErrorsAndCountMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer bad" {
			http.Error(w, `{"message":"invalid api token"}`, http.StatusUnauthorized)
			return
		}
		io.WriteString(w, `{"embeddings":{"float":[[1,0]]}}`)
	}))
	defer srv.Close()
	if _, err := NewCohereEmbed(srv.URL, "bad", "m", CohereSearchQuery, 0, 0).Embed(context.Background(), []string{"a"}); err == nil {
		t.Fatal("401 accepted")
	}
	if _, err := NewCohereEmbed(srv.URL, "k", "m", CohereSearchQuery, 0, 0).Embed(context.Background(), []string{"a", "b"}); err == nil {
		t.Fatal("1 vector for 2 inputs accepted")
	}
}

func TestCoherePacing(t *testing.T) {
	e := NewCohereEmbed("http://unused", "k", "m", CohereSearchDocument, 0, 0)
	now := time.Unix(1000, 0)
	var slept []time.Duration
	e.now = func() time.Time { return now }
	e.sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); now = now.Add(d); return nil }
	ctx := context.Background()
	if err := e.pace(ctx, tuning.CohereInputsPerMin-10); err != nil || len(slept) != 0 {
		t.Fatalf("first window: %v %v", err, slept)
	}
	now = now.Add(20 * time.Second)
	if err := e.pace(ctx, 20); err != nil || len(slept) != 1 || slept[0] != 40*time.Second {
		t.Fatalf("over the limit must wait for the window: %v %v", err, slept)
	}
	if err := e.pace(ctx, 5); err != nil || len(slept) != 1 {
		t.Fatalf("new window must not wait: %v %v", err, slept)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	e.sleep = sleepCtx
	e.winInputs = tuning.CohereInputsPerMin
	if err := e.pace(cancelled, 1); err == nil {
		t.Fatal("cancelled context must stop pacing")
	}
}
