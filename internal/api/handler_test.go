package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"olake-ask/internal/domain"
	"olake-ask/internal/middleware"
	"olake-ask/internal/tuning"
)

type fakeAnswerer struct{ res domain.Result }

func (f fakeAnswerer) Answer(context.Context, string) domain.Result { return f.res }

func router(t *testing.T, res domain.Result) http.Handler {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r, err := NewRouter(Options{
		Answerer:       fakeAnswerer{res: res},
		Health:         HealthResponse{IndexVersion: "v1", Chunks: 3, Vectors: true},
		AllowedOrigins: []string{"https://olake.io"},
		RateLimiter:    middleware.NewRateLimiter(ctx),
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func do(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(w, req)
	return w
}

func TestAskExactKeysAndEmptySources(t *testing.T) {
	h := router(t, domain.Result{Answer: "I don't know.", Mode: domain.ModeDontKnow}) // Sources nil
	w := do(h, http.MethodPost, "/ask", `{"question":"what is olake?","extra":1}`)
	if w.Code != http.StatusOK || w.Header().Get("X-Ask-Mode") != "dont_know" || w.Header().Get("X-Index-Version") != "v1" {
		t.Fatalf("code=%d headers=%v", w.Code, w.Header())
	}
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 || m["answer"] != "I don't know." {
		t.Fatalf("keys = %v", m)
	}
	if s, ok := m["sources"].([]any); !ok || len(s) != 0 {
		t.Fatalf("sources = %#v (body %s)", m["sources"], w.Body.String())
	}
}

func TestAskErrors(t *testing.T) {
	h := router(t, domain.Result{Mode: domain.ModeAnswered})
	cases := []struct {
		body string
		code int
		msg  string
	}{
		{`not json`, 400, "invalid JSON body"},
		{`{"question":"   "}`, 400, "question is required"},
		{`{"question":"` + strings.Repeat("é", tuning.MaxQuestionChars+1) + `"}`, 400, "question too long (max 2000 characters)"},
		{`{"question":"` + strings.Repeat("x", tuning.MaxBodyBytes) + `"}`, 413, "request too large"},
	}
	for _, c := range cases {
		w := do(h, http.MethodPost, "/ask", c.body)
		var e ErrorResponse
		json.Unmarshal(w.Body.Bytes(), &e)
		if w.Code != c.code || e.Error != c.msg {
			t.Errorf("body %.30q: %d %q", c.body, w.Code, w.Body.String())
		}
	}
}

func TestAskRateLimited(t *testing.T) {
	h := router(t, domain.Result{Mode: domain.ModeAnswered})
	var last *httptest.ResponseRecorder
	for i := 0; i <= tuning.RateBurst; i++ {
		last = do(h, http.MethodPost, "/ask", `{"question":"how to sync?"}`)
	}
	if last.Code != http.StatusTooManyRequests || !strings.Contains(last.Body.String(), "Too many questions") {
		t.Fatalf("last = %d %s", last.Code, last.Body.String())
	}
}

func TestHealthRootWidgetNotFound(t *testing.T) {
	h := router(t, domain.Result{})
	w := do(h, http.MethodGet, "/healthz", "")
	var hr HealthResponse
	json.Unmarshal(w.Body.Bytes(), &hr)
	if w.Code != 200 || hr.Status != "ok" || hr.IndexVersion != "v1" || hr.Chunks != 3 || !hr.Vectors || hr.LLM {
		t.Fatalf("healthz = %d %s", w.Code, w.Body.String())
	}
	if w := do(h, http.MethodHead, "/healthz", ""); w.Code != 200 || w.Body.Len() != 0 {
		t.Fatalf("HEAD healthz = %d %q", w.Code, w.Body.String())
	}
	if w := do(h, http.MethodGet, "/", ""); w.Code != 200 || w.Body.String() != "Ask OLake API. POST /ask" || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("root = %d %q", w.Code, w.Body.String())
	}
	if w := do(h, http.MethodHead, "/", ""); w.Code != 200 {
		t.Fatalf("HEAD root = %d", w.Code)
	}
	w = do(h, http.MethodGet, "/widget.js", "")
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/javascript; charset=utf-8" || w.Header().Get("Cache-Control") != "public, max-age=3600" {
		t.Fatalf("widget = %d %v", w.Code, w.Header())
	}
	if w := do(h, http.MethodGet, "/nope", ""); w.Code != 404 || !strings.Contains(w.Body.String(), `"not found"`) {
		t.Fatalf("404 = %d %s", w.Code, w.Body.String())
	}
	if w := do(h, http.MethodOptions, "/ask", ""); w.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS = %d", w.Code)
	}
}
