// Package utils holds text, URL, redaction and retry helpers shared by the
// seeder and the server. The tokenizer here is the single source of truth for
// both index time and query time.
package utils

import (
	"regexp"
	"strings"
	"unicode"

	"olake-ask/internal/tuning"
)

var (
	spaceRunRe = regexp.MustCompile(`[ \t]+`)
	rawTokenRe = regexp.MustCompile(`[a-z0-9]+(?:[._\-/][a-z0-9]+)*`)
	wordRe     = regexp.MustCompile(`[A-Za-z0-9]+`)
	partSepRe  = regexp.MustCompile(`[._\-/]`)
)

// IdentifierRe matches dotted/underscored/hyphenated or camelCase identifiers.
var IdentifierRe = regexp.MustCompile("^[A-Za-z][A-Za-z0-9]*(?:[._\\-][A-Za-z0-9]+)+$|^[a-z]+[A-Z][A-Za-z0-9]*$")

var stopwords = toSet(`a an the is are was were be been being am to of in on for with and or not how what why
when where which who whom whose do does did doing done can could should would will shall may might must i me my
mine we us our ours you your yours he him his she her it its they them their this that these those there here as
at by from into onto about if then than so but also just any all some use using used uses get gets set need needs
want wants please help has have had having very too such only own same other each both few more most because
while during before after again further once out off through between until like way let lets make show tell
explain know thanks thank hi hello hey up`)

func toSet(s string) map[string]struct{} {
	m := make(map[string]struct{})
	for _, w := range strings.Fields(s) {
		m[w] = struct{}{}
	}
	return m
}

// IsStopword reports whether a lowercased token is a stopword.
func IsStopword(t string) bool {
	_, ok := stopwords[t]
	return ok
}

// Normalize lowercases, normalizes newlines, collapses spaces/tabs and trims.
func Normalize(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = spaceRunRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// camelParts splits camelCase words (jobMapping → job, mapping) of the
// original-case text. Words without an internal case boundary yield nothing.
func camelParts(s string) []string {
	var out []string
	for _, w := range wordRe.FindAllString(s, -1) {
		parts := splitCamel(w)
		if len(parts) < 2 || hasOneLetter(parts) {
			continue // OLake is a name, not "o" + "lake"
		}
		out = append(out, parts...)
	}
	return out
}

func hasOneLetter(parts []string) bool {
	for _, p := range parts {
		if len([]rune(p)) == 1 {
			return true
		}
	}
	return false
}

func splitCamel(w string) []string {
	rs := []rune(w)
	var parts []string
	start := 0
	for i := 1; i < len(rs); i++ {
		prev, cur := rs[i-1], rs[i]
		boundary := false
		switch {
		case unicode.IsLower(prev) && unicode.IsUpper(cur):
			boundary = true
		case unicode.IsUpper(prev) && unicode.IsUpper(cur) && i+1 < len(rs) && unicode.IsLower(rs[i+1]):
			boundary = true // HTTPServer → HTTP, Server
		}
		if boundary {
			parts = append(parts, string(rs[start:i]))
			start = i
		}
	}
	return append(parts, string(rs[start:]))
}

// fold applies the light plural fold.
func fold(t string) string {
	if len(t) > 4 {
		if strings.HasSuffix(t, "ies") {
			return t[:len(t)-3] + "y"
		}
		if strings.HasSuffix(t, "s") && !strings.HasSuffix(t, "ss") &&
			!strings.HasSuffix(t, "us") && !strings.HasSuffix(t, "is") {
			return t[:len(t)-1]
		}
	}
	return t
}

func isDigits(t string) bool {
	if t == "" {
		return false
	}
	for _, r := range t {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func keep(t string, out []string) []string {
	if IsStopword(t) {
		return out
	}
	t = fold(t)
	if len(t) < 2 && !isDigits(t) {
		return out
	}
	return append(out, t)
}

// Tokenize turns text into search tokens: compound tokens plus their parts,
// camelCase parts, minus stopwords, with a light plural fold.
func Tokenize(s string) []string {
	camel := camelParts(s)
	lower := strings.ToLower(s)
	var out []string
	for _, tok := range rawTokenRe.FindAllString(lower, -1) {
		out = keep(tok, out)
		if partSepRe.MatchString(tok) {
			for _, p := range partSepRe.Split(tok, -1) {
				if len(p) >= 2 {
					out = keep(p, out)
				}
			}
		}
	}
	for _, p := range camel {
		p = strings.ToLower(p)
		if len(p) >= 2 {
			out = keep(p, out)
		}
	}
	return out
}

// ContentTerms returns unique tokens of s in order, capped at MaxQueryTerms.
func ContentTerms(s string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, t := range Tokenize(s) {
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
		if len(out) >= tuning.MaxQueryTerms {
			break
		}
	}
	return out
}

// ExtractIdentifiers returns unique identifier-looking words in s.
func ExtractIdentifiers(s string) []string {
	words := strings.FieldsFunc(s, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-')
	})
	seen := make(map[string]struct{})
	var out []string
	for _, w := range words {
		w = strings.Trim(w, "._-")
		if w == "" || !IdentifierRe.MatchString(w) {
			continue
		}
		if _, ok := seen[w]; ok {
			continue
		}
		seen[w] = struct{}{}
		out = append(out, w)
	}
	return out
}

// NonASCIIRatio returns letters outside ASCII divided by all letters.
func NonASCIIRatio(s string) float64 {
	var all, non int
	for _, r := range s {
		if !unicode.IsLetter(r) {
			continue
		}
		all++
		if r > unicode.MaxASCII {
			non++
		}
	}
	if all == 0 {
		return 0
	}
	return float64(non) / float64(all)
}

// Words splits on whitespace.
func Words(s string) []string { return strings.Fields(s) }

// TruncateWords returns the first n words of s and whether it was cut.
func TruncateWords(s string, n int) (string, bool) {
	w := strings.Fields(s)
	if len(w) <= n {
		return strings.Join(w, " "), false
	}
	return strings.Join(w[:n], " "), true
}

// TruncateChars cuts s to at most max bytes at a word boundary (rune-safe)
// and reports whether it was cut.
func TruncateChars(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	cut := max
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	if i := strings.LastIndexAny(s[:cut], " \n\t"); i > max/2 {
		cut = i
	}
	return strings.TrimRight(s[:cut], " \n\t"), true
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// errPatternGap joins the static pieces of an error-message pattern.
const errPatternGap = ".{0,200}?"

var formatVerbRe = regexp.MustCompile(`%[-+# 0]*[0-9*]*(?:\.[0-9*]+)?[a-zA-Z%]`)

// ErrorPieces splits a Go format string into its trimmed, non-empty static pieces.
func ErrorPieces(msg string) []string {
	var out []string
	for _, p := range formatVerbRe.Split(msg, -1) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ErrorPattern builds the case-insensitive regex matching an instantiated
// message from its static pieces.
func ErrorPattern(pieces []string) string {
	q := make([]string, len(pieces))
	for i, p := range pieces {
		q[i] = regexp.QuoteMeta(p)
	}
	return "(?i)" + strings.Join(q, errPatternGap)
}

// ErrorLiterals recovers the static pieces from a pattern built by ErrorPattern.
func ErrorLiterals(pattern string) []string {
	var out []string
	for _, q := range strings.Split(strings.TrimPrefix(pattern, "(?i)"), errPatternGap) {
		var b strings.Builder
		for i := 0; i < len(q); i++ {
			if q[i] == '\\' && i+1 < len(q) {
				i++
			}
			b.WriteByte(q[i])
		}
		out = append(out, b.String())
	}
	return out
}

// Longest returns the longest string in ss.
func Longest(ss []string) string {
	best := ""
	for _, s := range ss {
		if len(s) > len(best) {
			best = s
		}
	}
	return best
}
