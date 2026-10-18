package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var serverVars = []string{"LLM_PROVIDER", "LLM_BASE_URL", "LLM_MODEL", "LLM_FALLBACK_MODEL", "LLM_FALLBACK_MODELS", "LLM_REASONING_EFFORT", "LLM_API_KEY",
	"EMBED_BASE_URL", "EMBED_API_KEY", "VECTOR_BACKEND", "REWRITE", "RERANK", "DAILY_TOKEN_BUDGET",
	"ALLOWED_ORIGINS", "PORT", "DEBUG", "EMBEDDER", "EMBED_MODEL", "SEED_ISSUES", "GITHUB_TOKEN",
	"EMBED_API", "EMBED_API_MODEL", "EMBED_API_DIM", "EMBED_API_BASE_URL"}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range serverVars {
		t.Setenv(k, "")
	}
}

func TestServerDefaults(t *testing.T) {
	clearEnv(t)
	s, err := LoadServer()
	if err != nil {
		t.Fatal(err)
	}
	if s.LLMProvider != "openai_compatible" || s.VectorBackend != "memory" || s.Rewrite != "off" || s.Rerank ||
		s.DailyTokenBudget != 5000000 || s.Port != 8080 || s.LLMEnabled() || s.Debug ||
		s.LLMReasoning != "none" || s.LLMBaseURL != defaultOpenAIURL || s.LLMModel != defaultOpenAIModel ||
		!reflect.DeepEqual(s.LLMFallbackModels, []string{"google/gemini-2.5-flash-lite", "deepseek/deepseek-v4.1-flash"}) {
		t.Fatalf("unexpected defaults: %+v", s)
	}
	want := []string{"https://olake.io", "http://localhost:*", "http://127.0.0.1:*"}
	if !reflect.DeepEqual(s.AllowedOrigins, want) {
		t.Fatalf("origins = %v", s.AllowedOrigins)
	}
}

func TestServerLLMEnabled(t *testing.T) {
	clearEnv(t)
	t.Setenv("LLM_API_KEY", "k")
	s, _ := LoadServer()
	if !s.LLMEnabled() {
		t.Fatalf("openai_compatible with only a key must use the OpenRouter defaults: %+v", s)
	}
	t.Setenv("LLM_PROVIDER", "anthropic")
	s, _ = LoadServer()
	if s.LLMEnabled() || s.LLMFallbackModels != nil {
		t.Fatalf("anthropic must not inherit OpenRouter model defaults: %+v", s)
	}
	t.Setenv("LLM_MODEL", "m")
	s, _ = LoadServer()
	if !s.LLMEnabled() || s.LLMBaseURL != defaultAnthropicURL {
		t.Fatalf("anthropic default base: %+v", s)
	}
}

func TestServerFallbackModels(t *testing.T) {
	clearEnv(t)
	t.Setenv("LLM_FALLBACK_MODELS", " google/gemini-2.5-flash-lite, ,deepseek/deepseek-v4.1-flash ")
	s, err := LoadServer()
	want := []string{"google/gemini-2.5-flash-lite", "deepseek/deepseek-v4.1-flash"}
	if err != nil || !reflect.DeepEqual(s.LLMFallbackModels, want) {
		t.Fatalf("models = %v err = %v", s.LLMFallbackModels, err)
	}
	t.Setenv("LLM_FALLBACK_MODELS", "none")
	if s, _ = LoadServer(); s.LLMFallbackModels != nil {
		t.Fatalf("none must disable fallbacks: %v", s.LLMFallbackModels)
	}
}

func TestServerInvalid(t *testing.T) {
	cases := map[string]string{
		"LLM_PROVIDER":         "gemini",
		"VECTOR_BACKEND":       "qdrant",
		"REWRITE":              "sometimes",
		"RERANK":               "yes",
		"DAILY_TOKEN_BUDGET":   "-1",
		"PORT":                 "abc",
		"ALLOWED_ORIGINS":      "https://*.olake.io",
		"LLM_BASE_URL":         "ftp://x",
		"DEBUG":                "true",
		"LLM_REASONING_EFFORT": "max",
	}
	for k, v := range cases {
		clearEnv(t)
		t.Setenv(k, v)
		_, err := LoadServer()
		if err == nil || !strings.Contains(err.Error(), k) {
			t.Errorf("%s=%s: err = %v, want error naming the var", k, v, err)
		}
	}
	clearEnv(t)
	t.Setenv("VECTOR_BACKEND", "qdrant")
	if _, err := LoadServer(); err == nil || !strings.Contains(err.Error(), "unsupported VECTOR_BACKEND") {
		t.Fatalf("err = %v", err)
	}
}

func TestSeeder(t *testing.T) {
	clearEnv(t)
	s, err := LoadSeeder()
	if err != nil || s.Embedder != "model2vec" || s.EmbedModel != "RETRIEVAL32M" || !s.SeedIssues ||
		s.EmbedAPI != "auto" || s.EmbedAPIModel != "" || s.EmbedAPIDim != 0 || s.EmbedAPIBaseURL != defaultCohereURL || s.CohereEnabled() {
		t.Fatalf("seeder defaults: %+v %v", s, err)
	}
	t.Setenv("EMBEDDER", "faiss")
	if _, err := LoadSeeder(); err == nil {
		t.Fatal("invalid EMBEDDER accepted")
	}
	t.Setenv("EMBEDDER", "none")
	t.Setenv("SEED_ISSUES", "off")
	if s, _ := LoadSeeder(); s.Embedder != "none" || s.SeedIssues {
		t.Fatalf("seeder: %+v", s)
	}
}

func TestLoadDotEnv(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	body := "# comment\n\nOLAKE_TEST_A=1\nexport OLAKE_TEST_B=\"two\"\nOLAKE_TEST_C=from-file\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OLAKE_TEST_C", "from-env")
	os.Unsetenv("OLAKE_TEST_A")
	os.Unsetenv("OLAKE_TEST_B")
	t.Cleanup(func() { os.Unsetenv("OLAKE_TEST_A"); os.Unsetenv("OLAKE_TEST_B") })
	if err := LoadDotEnv(p); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("OLAKE_TEST_A") != "1" || os.Getenv("OLAKE_TEST_B") != "two" || os.Getenv("OLAKE_TEST_C") != "from-env" {
		t.Fatal("dotenv values wrong")
	}
	if err := LoadDotEnv(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Fatal(err)
	}
}

func TestServerDebug(t *testing.T) {
	clearEnv(t)
	t.Setenv("DEBUG", "on")
	if s, err := LoadServer(); err != nil || !s.Debug {
		t.Fatalf("DEBUG=on: %+v %v", s, err)
	}
}

func TestEmbedAPIConfig(t *testing.T) {
	clearEnv(t)
	s, err := LoadServer()
	if err != nil || s.EmbedAPI != "auto" || s.EmbedAPIBaseURL != defaultCohereURL {
		t.Fatalf("server defaults: %+v %v", s, err)
	}
	t.Setenv("EMBED_API", "off")
	if s, _ := LoadServer(); s.EmbedAPI != "off" {
		t.Fatalf("EMBED_API=off: %+v", s)
	}

	// Cohere vectors need EMBEDDER=model2vec, EMBED_API=auto, a model and a key.
	cases := []struct {
		env  map[string]string
		want bool
	}{
		{map[string]string{"EMBED_API_MODEL": "embed-v4.0"}, false},
		{map[string]string{"EMBED_API_KEY": "k"}, false},
		{map[string]string{"EMBED_API_MODEL": "embed-v4.0", "EMBED_API_KEY": "k"}, true},
		{map[string]string{"EMBED_API_MODEL": "embed-v4.0", "EMBED_API_KEY": "k", "EMBED_API": "off"}, false},
		{map[string]string{"EMBED_API_MODEL": "embed-v4.0", "EMBED_API_KEY": "k", "EMBEDDER": "none"}, false},
		// Legacy OpenAI-compatible mode shares EMBED_API_KEY: still valid, never Cohere.
		{map[string]string{"EMBED_API_MODEL": "embed-v4.0", "EMBED_API_KEY": "k", "EMBEDDER": "api", "EMBED_BASE_URL": "https://x/v1"}, false},
	}
	for _, c := range cases {
		clearEnv(t)
		for k, v := range c.env {
			t.Setenv(k, v)
		}
		s, err := LoadSeeder()
		if err != nil || s.CohereEnabled() != c.want {
			t.Errorf("%v: enabled=%v err=%v, want %v", c.env, s.CohereEnabled(), err, c.want)
		}
	}

	for k, v := range map[string]string{"EMBED_API": "on", "EMBED_API_DIM": "-1", "EMBED_API_BASE_URL": "ftp://x"} {
		clearEnv(t)
		t.Setenv(k, v)
		if _, err := LoadSeeder(); err == nil || !strings.Contains(err.Error(), k) {
			t.Errorf("seeder %s=%s: err = %v", k, v, err)
		}
		if k == "EMBED_API_DIM" {
			continue // seeder-only setting
		}
		if _, err := LoadServer(); err == nil || !strings.Contains(err.Error(), k) {
			t.Errorf("server %s=%s: err = %v", k, v, err)
		}
	}
}
