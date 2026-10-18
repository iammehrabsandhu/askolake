package utils

import (
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"olake-ask/internal/tuning"
)

func TestTokenizeCompoundAndParts(t *testing.T) {
	got := Tokenize("replication_slot and config.yaml")
	for _, want := range []string{"replication_slot", "replication", "slot", "config.yaml", "config", "yaml"} {
		if !slices.Contains(got, want) {
			t.Errorf("Tokenize missing %q in %v", want, got)
		}
	}
	if slices.Contains(got, "and") {
		t.Errorf("stopword kept: %v", got)
	}
}

func TestTokenizeCamelCase(t *testing.T) {
	got := Tokenize("global.jobMapping HTTPServer")
	for _, want := range []string{"jobmapping", "job", "mapping", "global", "http", "server"} {
		if !slices.Contains(got, want) {
			t.Errorf("Tokenize missing %q in %v", want, got)
		}
	}
}

// "OLake" is a name: no extra "lake" term. Regular camelCase still splits.
func TestTokenizeNoOneLetterCamelSplit(t *testing.T) {
	got := Tokenize("How do I set up OLake with PostgreSQL?")
	if slices.Contains(got, "lake") || slices.Contains(got, "up") || !slices.Contains(got, "olake") {
		t.Errorf("Tokenize = %v", got)
	}
	if !slices.Contains(got, "postgre") || !slices.Contains(got, "sql") {
		t.Errorf("PostgreSQL parts missing: %v", got)
	}
}

func TestTokenizePluralFoldAndStopwords(t *testing.T) {
	got := Tokenize("How do I use the policies for files, class and status? Version 3")
	want := []string{"policy", "file", "class", "status", "version", "3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Tokenize = %v, want %v", got, want)
	}
	if !slices.Contains(Tokenize("olake sync"), "olake") {
		t.Fatal("olake must not be a stopword")
	}
}

func TestContentTermsUniqueAndCapped(t *testing.T) {
	if got := ContentTerms("sync sync sync mode"); !reflect.DeepEqual(got, []string{"sync", "mode"}) {
		t.Fatalf("ContentTerms = %v", got)
	}
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString("term")
		b.WriteString(strings.Repeat("x", i%7))
		b.WriteString(string(rune('a'+i%26)) + string(rune('a'+i/26)) + " ")
	}
	if got := ContentTerms(b.String()); len(got) != tuning.MaxQueryTerms {
		t.Fatalf("ContentTerms len = %d, want %d", len(got), tuning.MaxQueryTerms)
	}
}

func TestExtractIdentifiers(t *testing.T) {
	got := ExtractIdentifiers("Set global.jobMapping and replication_slot, then max-threads. Use jobMapping (plain words ignored).")
	want := []string{"global.jobMapping", "replication_slot", "max-threads", "jobMapping"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExtractIdentifiers = %v, want %v", got, want)
	}
}

func TestNonASCIIRatio(t *testing.T) {
	if r := NonASCIIRatio("hello"); r != 0 {
		t.Fatalf("ascii ratio = %v", r)
	}
	if r := NonASCIIRatio("привет"); r != 1 {
		t.Fatalf("cyrillic ratio = %v", r)
	}
	if r := NonASCIIRatio("123 !!"); r != 0 {
		t.Fatalf("no letters ratio = %v", r)
	}
}

func TestNormalize(t *testing.T) {
	if got := Normalize("  Hello\t\tWorld \r\nNext  "); got != "hello world \nnext" {
		t.Fatalf("Normalize = %q", got)
	}
}

func TestRedact(t *testing.T) {
	cases := map[string]string{
		"key -----BEGIN RSA PRIVATE KEY-----\nabc\n-----END RSA PRIVATE KEY----- end": "key [REDACTED] end",
		"id AKIAABCDEFGHIJKLMNOP here":                             "id [REDACTED] here",
		"postgres://admin:hunter2@db:5432/x":                       "postgres://admin:[REDACTED]@db:5432/x",
		`{"password": "s3cret", "user": "a"}`:                      `{"password": "[REDACTED]", "user": "a"}`,
		"api_key=abcdef123":                                        "api_key=[REDACTED]",
		"Authorization: Bearer abc.def-ghi":                        "Authorization: Bearer [REDACTED]",
		"tok ghp_" + strings.Repeat("a", 36):                       "tok [REDACTED]",
		"tok github_pat_" + strings.Repeat("b", 30):                "tok [REDACTED]",
		"slack xoxb-1234567890-abc":                                "slack [REDACTED]",
		"openai sk-" + strings.Repeat("c", 24):                     "openai [REDACTED]",
		"jwt eyJhbGciOiJIUzI1.eyJzdWIiOiIxMjM0.SflKxwRJSMeKKF2QT4": "jwt [REDACTED]",
	}
	for in, want := range cases {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
	q := "how do I set the password for postgres?"
	if got := Redact(q); got != q {
		t.Errorf("benign question changed: %q", got)
	}
}

func TestGitHubSlug(t *testing.T) {
	if got := GitHubSlug("Getting Started: Step 1 (Docker)"); got != "getting-started-step-1-docker" {
		t.Fatalf("GitHubSlug = %q", got)
	}
	if got := GitHubSlug("max_threads & co-op"); got != "max_threads--co-op" {
		t.Fatalf("GitHubSlug = %q", got)
	}
}

func TestIsTrustedHost(t *testing.T) {
	trusted := []string{"https://olake.io/docs/", "https://www.olake.io/x", "https://github.com/datazip-inc/olake/issues/1"}
	untrusted := []string{"https://evil.com/olake.io", "https://github.com/other/olake", "ftp://olake.io/", "not a url"}
	for _, u := range trusted {
		if !IsTrustedHost(u) {
			t.Errorf("%s should be trusted", u)
		}
	}
	for _, u := range untrusted {
		if IsTrustedHost(u) {
			t.Errorf("%s should not be trusted", u)
		}
	}
}

func TestURLHelpers(t *testing.T) {
	if StripFragment("https://a/b#c") != "https://a/b" || HostOf("https://OLake.io/x") != "olake.io" {
		t.Fatal("url helpers")
	}
	if !IsHTTPURL("https://a.b/") || IsHTTPURL("/relative") {
		t.Fatal("IsHTTPURL")
	}
}

func TestBackoffAndRetryAfter(t *testing.T) {
	for i := 0; i < 10; i++ {
		if d := Backoff(i); d < tuning.RetryBase || d > tuning.RetryMax {
			t.Fatalf("Backoff(%d) = %v", i, d)
		}
	}
	h := http.Header{}
	h.Set("Retry-After", "2")
	if d, ok := RetryAfter(h); !ok || d != 2*time.Second {
		t.Fatalf("RetryAfter seconds = %v %v", d, ok)
	}
	h.Set("Retry-After", "120")
	if d, _ := RetryAfter(h); d != tuning.RetryMax {
		t.Fatalf("RetryAfter cap = %v", d)
	}
	h.Set("Retry-After", time.Now().Add(time.Second).UTC().Format(http.TimeFormat))
	if d, ok := RetryAfter(h); !ok || d > 2*time.Second {
		t.Fatalf("RetryAfter date = %v %v", d, ok)
	}
}

func TestTruncate(t *testing.T) {
	s, cut := TruncateChars("hello wonderful world", 17)
	if !cut || s != "hello wonderful" {
		t.Fatalf("TruncateChars = %q %v", s, cut)
	}
	w, cut := TruncateWords("a b c d", 2)
	if !cut || w != "a b" {
		t.Fatalf("TruncateWords = %q", w)
	}
}

func TestErrorPatternRoundTrip(t *testing.T) {
	pieces := ErrorPieces("failed to connect to %s: replication slot (%q) not found %d%%")
	want := []string{"failed to connect to", ": replication slot (", ") not found"}
	if !reflect.DeepEqual(pieces, want) {
		t.Fatalf("pieces = %q", pieces)
	}
	pat := ErrorPattern(pieces)
	if got := ErrorLiterals(pat); !reflect.DeepEqual(got, want) {
		t.Fatalf("literals = %q", got)
	}
	if Longest(pieces) != "failed to connect to" {
		t.Fatal("Longest")
	}
}
