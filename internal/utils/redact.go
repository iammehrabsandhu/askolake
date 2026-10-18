package utils

import "regexp"

const redacted = "[REDACTED]"

type redactRule struct {
	re   *regexp.Regexp
	repl string
}

var redactRules = []redactRule{
	{regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`), redacted},
	{regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`), redacted},
	{regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://[^:/\s@]+:)([^@\s/]+)(@)`), "${1}" + redacted + "${3}"},
	{regexp.MustCompile(`(?i)("?(?:aws_secret_access_key|secret_access_key|secret_key|client_secret|secret|password|passwd|pwd|token|access_token|api[_-]?key|access[_-]?key)"?\s*[:=]\s*"?)([^"'\s,}]+)`), "${1}" + redacted},
	{regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/\-]+=*`), "${1}" + redacted},
	{regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}\b`), redacted},
	{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}\b`), redacted},
	{regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`), redacted},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_\-]{20,}\b`), redacted},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`), redacted},
}

// Redact replaces secret-looking values in s with [REDACTED].
func Redact(s string) string {
	for _, r := range redactRules {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}
