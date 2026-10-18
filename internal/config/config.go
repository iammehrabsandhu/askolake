// Package config parses settings and secrets from the environment.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Server is the configuration of cmd/server and cmd/eval.
type Server struct {
	LLMProvider      string // openai_compatible | anthropic
	LLMBaseURL       string // resolved (anthropic default applied)
	LLMModel         string
	LLMFallbackModel string
	LLMAPIKey        string

	EmbedBaseURL string
	EmbedAPIKey  string
	// Optional Cohere vectors (bundle api_embedder): used when EMBED_API=auto
	// and EMBED_API_KEY is set; otherwise the local vectors are used.
	EmbedAPI        string // auto | off
	EmbedAPIBaseURL string

	VectorBackend    string // memory
	Rewrite          string // off | when_needed | always
	Rerank           bool
	DailyTokenBudget int // 0 = unlimited
	AllowedOrigins   []string
	Port             int
	Debug            bool // DEBUG=on prints outgoing LLM requests to the terminal
}

// LLMEnabled reports whether an LLM is fully configured.
func (s Server) LLMEnabled() bool {
	return s.LLMAPIKey != "" && s.LLMModel != "" && s.LLMBaseURL != ""
}

// Seeder is the configuration of cmd/seeder.
type Seeder struct {
	GitHubToken  string
	Embedder     string // model2vec | api | none
	EmbedModel   string
	EmbedBaseURL string
	EmbedAPIKey  string
	SeedIssues   bool
	// Optional second (Cohere) vectors next to the local model2vec ones.
	EmbedAPI        string // auto | off
	EmbedAPIModel   string // e.g. embed-v4.0; empty = no Cohere vectors
	EmbedAPIDim     int    // output_dimension; 0 = model default
	EmbedAPIBaseURL string
}

// CohereEnabled reports whether the seeder also builds Cohere vectors. It
// needs local model2vec vectors to fall back to, so the legacy EMBEDDER=api
// mode never turns it on.
func (s Seeder) CohereEnabled() bool {
	return s.Embedder == "model2vec" && s.EmbedAPI == "auto" && s.EmbedAPIModel != "" && s.EmbedAPIKey != ""
}

const (
	defaultOrigins      = "https://olake.io,http://localhost:*,http://127.0.0.1:*"
	defaultAnthropicURL = "https://api.anthropic.com"
	defaultCohereURL    = "https://api.cohere.com/v2"
	defaultEmbedModel   = "RETRIEVAL32M"
)

// embedAPI reads EMBED_API and EMBED_API_BASE_URL (shared by server and seeder).
func embedAPI() (mode, baseURL string, err error) {
	if mode, err = oneOf("EMBED_API", "auto", "auto", "off"); err != nil {
		return "", "", err
	}
	baseURL = env("EMBED_API_BASE_URL", defaultCohereURL)
	if !isHTTP(baseURL) {
		return "", "", fmt.Errorf("EMBED_API_BASE_URL: %q is not an http(s) URL", baseURL)
	}
	return mode, baseURL, nil
}

// LoadServer reads the server configuration.
func LoadServer() (Server, error) {
	if err := LoadDotEnv(".env"); err != nil {
		return Server{}, err
	}
	var s Server
	var err error
	if s.LLMProvider, err = oneOf("LLM_PROVIDER", "openai_compatible", "openai_compatible", "anthropic"); err != nil {
		return s, err
	}
	s.LLMBaseURL = env("LLM_BASE_URL", "")
	if s.LLMBaseURL == "" && s.LLMProvider == "anthropic" {
		s.LLMBaseURL = defaultAnthropicURL
	}
	if s.LLMBaseURL != "" && !isHTTP(s.LLMBaseURL) {
		return s, fmt.Errorf("LLM_BASE_URL: %q is not an http(s) URL", s.LLMBaseURL)
	}
	s.LLMModel = env("LLM_MODEL", "")
	s.LLMFallbackModel = env("LLM_FALLBACK_MODEL", "")
	s.LLMAPIKey = env("LLM_API_KEY", "")

	s.EmbedBaseURL = env("EMBED_BASE_URL", "")
	if s.EmbedBaseURL != "" && !isHTTP(s.EmbedBaseURL) {
		return s, fmt.Errorf("EMBED_BASE_URL: %q is not an http(s) URL", s.EmbedBaseURL)
	}
	s.EmbedAPIKey = env("EMBED_API_KEY", "")
	if s.EmbedAPI, s.EmbedAPIBaseURL, err = embedAPI(); err != nil {
		return s, err
	}

	s.VectorBackend = env("VECTOR_BACKEND", "memory")
	if s.VectorBackend != "memory" {
		return s, fmt.Errorf("VECTOR_BACKEND: unsupported VECTOR_BACKEND %q (allowed: memory)", s.VectorBackend)
	}
	if s.Rewrite, err = oneOf("REWRITE", "off", "off", "when_needed", "always"); err != nil {
		return s, err
	}
	rerank, err := oneOf("RERANK", "off", "off", "on")
	if err != nil {
		return s, err
	}
	s.Rerank = rerank == "on"
	if s.DailyTokenBudget, err = intVar("DAILY_TOKEN_BUDGET", 5000000, 0); err != nil {
		return s, err
	}
	if s.AllowedOrigins, err = parseOrigins(env("ALLOWED_ORIGINS", defaultOrigins)); err != nil {
		return s, err
	}
	debug, err := oneOf("DEBUG", "off", "off", "on")
	if err != nil {
		return s, err
	}
	s.Debug = debug == "on"
	if s.Port, err = intVar("PORT", 8080, 1); err != nil {
		return s, err
	}
	if s.Port > 65535 {
		return s, fmt.Errorf("PORT: %d out of range", s.Port)
	}
	return s, nil
}

// LoadSeeder reads the seeder configuration.
func LoadSeeder() (Seeder, error) {
	if err := LoadDotEnv(".env"); err != nil {
		return Seeder{}, err
	}
	var s Seeder
	var err error
	s.GitHubToken = env("GITHUB_TOKEN", "")
	if s.Embedder, err = oneOf("EMBEDDER", "model2vec", "model2vec", "api", "none"); err != nil {
		return s, err
	}
	s.EmbedModel = env("EMBED_MODEL", defaultEmbedModel)
	s.EmbedBaseURL = env("EMBED_BASE_URL", "")
	if s.EmbedBaseURL != "" && !isHTTP(s.EmbedBaseURL) {
		return s, fmt.Errorf("EMBED_BASE_URL: %q is not an http(s) URL", s.EmbedBaseURL)
	}
	s.EmbedAPIKey = env("EMBED_API_KEY", "")
	if s.EmbedAPI, s.EmbedAPIBaseURL, err = embedAPI(); err != nil {
		return s, err
	}
	s.EmbedAPIModel = env("EMBED_API_MODEL", "")
	if s.EmbedAPIDim, err = intVar("EMBED_API_DIM", 0, 0); err != nil {
		return s, err
	}
	issues, err := oneOf("SEED_ISSUES", "on", "on", "off")
	if err != nil {
		return s, err
	}
	s.SeedIssues = issues == "on"
	return s, nil
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func oneOf(key, def string, allowed ...string) (string, error) {
	v := env(key, def)
	for _, a := range allowed {
		if v == a {
			return v, nil
		}
	}
	return "", fmt.Errorf("%s: invalid value %q (allowed: %s)", key, v, strings.Join(allowed, ", "))
}

func intVar(key string, def, min int) (int, error) {
	raw := env(key, "")
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not an integer", key, raw)
	}
	if n < min {
		return 0, fmt.Errorf("%s: %d must be >= %d", key, n, min)
	}
	return n, nil
}

func isHTTP(u string) bool {
	p, err := url.Parse(u)
	return err == nil && (p.Scheme == "http" || p.Scheme == "https") && p.Host != ""
}

// parseOrigins validates a comma list of origins; a port wildcard is only
// allowed as a ":*" suffix (e.g. http://localhost:*).
func parseOrigins(raw string) ([]string, error) {
	var out []string
	for _, o := range strings.Split(raw, ",") {
		o = strings.TrimRight(strings.TrimSpace(o), "/")
		if o == "" {
			continue
		}
		check := o
		if strings.Contains(o, "*") {
			if !strings.HasSuffix(o, ":*") || strings.Count(o, "*") != 1 {
				return nil, fmt.Errorf("ALLOWED_ORIGINS: %q: '*' is only allowed as a ':*' port suffix", o)
			}
			check = strings.TrimSuffix(o, ":*")
		}
		p, err := url.Parse(check)
		if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" || (p.Path != "" && p.Path != "/") {
			return nil, fmt.Errorf("ALLOWED_ORIGINS: %q is not a valid origin (scheme://host[:port])", o)
		}
		out = append(out, o)
	}
	if len(out) == 0 {
		return nil, errors.New("ALLOWED_ORIGINS: no origins configured")
	}
	return out, nil
}

// LoadDotEnv loads KEY=VALUE lines from path when it exists. Blank lines and
// '#' comments are ignored; already-set variables are never overridden.
func LoadDotEnv(path string) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		s := strings.TrimSpace(sc.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		s = strings.TrimPrefix(s, "export ")
		k, v, ok := strings.Cut(s, "=")
		if !ok {
			return fmt.Errorf("%s:%d: expected KEY=VALUE", path, line)
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		if _, set := os.LookupEnv(k); set {
			continue
		}
		if err := os.Setenv(k, v); err != nil {
			return fmt.Errorf("%s:%d: %w", path, line, err)
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	return nil
}
