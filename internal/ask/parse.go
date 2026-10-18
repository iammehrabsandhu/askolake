package ask

import (
	"regexp"
	"strconv"
	"strings"

	"olake-ask/internal/tuning"
)

var (
	answerableRe   = regexp.MustCompile(`(?im)^\s*\**ANSWERABLE\**\s*:\s*\**\s*(yes|no)\b`)
	answerableLine = regexp.MustCompile(`(?im)^\s*\**ANSWERABLE\**\s*:.*$`)
	answerMarkRe   = regexp.MustCompile(`(?im)^\s*\**ANSWER\**\s*:\**`)
	rewriteLeadRe  = regexp.MustCompile(`^(?:[-*•]+|\d+[.)])\s*`)
	rerankIDsRe    = regexp.MustCompile(`(?im)^\s*IDS\s*:\s*([0-9,\s]+)`)
	rerankConfRe   = regexp.MustCompile(`(?im)^\s*CONFIDENCE\s*:\s*(high|medium|low|none)`)
)

// ParseAnswer splits an answer completion into the ANSWERABLE flag and body.
// When the flag is missing it is inferred: yes iff the body has at least one
// valid citation (1..nPassages) after normalization.
func ParseAnswer(text string, nPassages int) (bool, string) {
	answerable, known := false, false
	if m := answerableRe.FindStringSubmatch(text); m != nil {
		answerable, known = strings.EqualFold(m[1], "yes"), true
	}
	body := ""
	stripped := answerableLine.ReplaceAllString(text, "")
	if loc := answerMarkRe.FindStringIndex(stripped); loc != nil {
		body = stripped[loc[1]:]
	} else {
		body = stripped
	}
	body = strings.TrimSpace(body)
	if !known {
		answerable = len(validCitations(normalizeCitations(body), nPassages)) > 0
	}
	return answerable, body
}

// ParseRewrite returns at most 3 cleaned query lines.
func ParseRewrite(text string) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		l = rewriteLeadRe.ReplaceAllString(l, "")
		l = strings.TrimSpace(strings.Trim(l, `"'“”‘’`+"`"))
		if l == "" || len(l) > tuning.MaxRewriteLineChars {
			continue
		}
		out = append(out, l)
		if len(out) == 3 {
			break
		}
	}
	return out
}

// ParseRerank parses "IDS: ..." and "CONFIDENCE: ..." lines.
func ParseRerank(text string) ([]int, string, bool) {
	idm := rerankIDsRe.FindStringSubmatch(text)
	cm := rerankConfRe.FindStringSubmatch(text)
	if idm == nil || cm == nil {
		return nil, "", false
	}
	var ids []int
	for _, f := range strings.FieldsFunc(idm[1], func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' || r == '\r' }) {
		if n, err := strconv.Atoi(f); err == nil {
			ids = append(ids, n)
		}
	}
	return ids, strings.ToLower(cm[1]), true
}
