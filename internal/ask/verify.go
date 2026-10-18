package ask

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"olake-ask/internal/data"
	"olake-ask/internal/domain"
	"olake-ask/internal/utils"
)

var (
	codeSpanRe   = regexp.MustCompile("(?s)```.*?```|`[^`\n]+`")
	fenceRe      = regexp.MustCompile("(?s)```.*?```")
	cjkCiteRe    = regexp.MustCompile(`【(\d+)】`)
	wordCiteRe   = regexp.MustCompile(`(?i)\[\s*(?:source|passage|ref|context)\s*:?\s*(\d+)\s*\]`)
	listCiteRe   = regexp.MustCompile(`\[(\d+(?:\s*,\s*\d+)+)\]`)
	citeRe       = regexp.MustCompile(`\[(\d+)\]`)
	citeRunRe    = regexp.MustCompile(`(?:\[\d+\])+`)
	mdLinkRe     = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^)\s]+)\)`)
	bareURLRe    = regexp.MustCompile(`https?://[^\s)\]>"']+`)
	inlineCodeRe = regexp.MustCompile("`([^`]{2,80})`")
	placeholder  = "\x00"
)

// Verdict is the outcome of answer verification.
type Verdict struct {
	OK      bool
	Answer  string
	Sources []string
}

// mapProse applies f to the text outside code fences and inline code spans.
func mapProse(s string, f func(string) string) string {
	var b strings.Builder
	last := 0
	for _, loc := range codeSpanRe.FindAllStringIndex(s, -1) {
		b.WriteString(f(s[last:loc[0]]))
		b.WriteString(s[loc[0]:loc[1]])
		last = loc[1]
	}
	b.WriteString(f(s[last:]))
	return b.String()
}

func normalizeCitations(s string) string {
	return mapProse(s, func(p string) string {
		p = cjkCiteRe.ReplaceAllString(p, "[$1]")
		p = wordCiteRe.ReplaceAllString(p, "[$1]")
		return listCiteRe.ReplaceAllStringFunc(p, func(m string) string {
			var b strings.Builder
			for _, n := range strings.Split(strings.Trim(m, "[]"), ",") {
				b.WriteString("[" + strings.TrimSpace(n) + "]")
			}
			return b.String()
		})
	})
}

// validCitations returns the in-range citation numbers in order of appearance.
func validCitations(s string, n int) []int {
	var out []int
	mapProse(s, func(p string) string {
		for _, m := range citeRe.FindAllStringSubmatch(p, -1) {
			if k, err := strconv.Atoi(m[1]); err == nil && k >= 1 && k <= n {
				out = append(out, k)
			}
		}
		return p
	})
	return out
}

// Verify enforces citations, URL and identifier allowlists and deprecation notes.
func Verify(answerable bool, body string, sel []domain.Chunk, question string, identifiers map[string]struct{}, d data.Data) Verdict {
	if !answerable {
		return Verdict{}
	}
	text := normalizeCitations(body)

	// Drop out-of-range markers.
	text = mapProse(text, func(p string) string {
		return citeRe.ReplaceAllStringFunc(p, func(m string) string {
			k, _ := strconv.Atoi(m[1 : len(m)-1])
			if k < 1 || k > len(sel) {
				return ""
			}
			return m
		})
	})
	cites := validCitations(text, len(sel))
	if len(cites) == 0 {
		return Verdict{}
	}

	// Renumber by first citation; the same URL shares one index.
	sources := []string{}
	byURL := map[string]int{}
	remap := map[int]int{}
	for _, k := range cites {
		if _, ok := remap[k]; ok {
			continue
		}
		u := sel[k-1].URL
		idx, ok := byURL[u]
		if !ok {
			sources = append(sources, u)
			idx = len(sources)
			byURL[u] = idx
		}
		remap[k] = idx
	}
	text = mapProse(text, func(p string) string {
		p = citeRe.ReplaceAllStringFunc(p, func(m string) string {
			k, _ := strconv.Atoi(m[1 : len(m)-1])
			return fmt.Sprintf("[%d]", remap[k])
		})
		return citeRunRe.ReplaceAllStringFunc(p, collapseRun)
	})

	// URL allowlist.
	text = mapProse(text, func(p string) string {
		p = mdLinkRe.ReplaceAllStringFunc(p, func(m string) string {
			sm := mdLinkRe.FindStringSubmatch(m)
			if urlAllowed(sm[2], sel) {
				return m
			}
			return sm[1]
		})
		// Protect allowed markdown links from the bare-URL pass.
		var kept []string
		p = mdLinkRe.ReplaceAllStringFunc(p, func(m string) string {
			kept = append(kept, m)
			return fmt.Sprintf("%sL%d%s", placeholder, len(kept)-1, placeholder)
		})
		p = bareURLRe.ReplaceAllStringFunc(p, func(u string) string {
			trimmed := strings.TrimRight(u, ".,;:!?")
			if urlAllowed(trimmed, sel) {
				return u
			}
			return u[len(trimmed):]
		})
		for i, m := range kept {
			p = strings.Replace(p, fmt.Sprintf("%sL%d%s", placeholder, i, placeholder), m, 1)
		}
		return p
	})

	// Unknown identifiers.
	var unknown []string
	seen := map[string]bool{}
	for _, m := range inlineCodeRe.FindAllStringSubmatch(fenceRe.ReplaceAllString(text, ""), -1) {
		id := strings.TrimSpace(m[1])
		if seen[id] || !utils.IdentifierRe.MatchString(id) || knownIdentifier(id, identifiers, sel) {
			continue
		}
		seen[id] = true
		unknown = append(unknown, "`"+id+"`")
	}
	text = strings.TrimSpace(text)
	answer := text
	if len(unknown) > 0 {
		text += "\n\nNote: not found in the OLake docs or code — verify before using: " + strings.Join(unknown, ", ") + "."
	}

	// Deprecated terms the answer mentions but the question did not.
	for _, dep := range d.Deprecated {
		re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(dep.Term) + `\b`)
		if re.MatchString(answer) && !re.MatchString(question) {
			text += fmt.Sprintf("\n\nNote: %s (%s)", dep.Note, dep.URL)
		}
	}
	return Verdict{OK: true, Answer: text, Sources: sources}
}

func collapseRun(run string) string {
	var b strings.Builder
	prev := ""
	for _, m := range citeRe.FindAllString(run, -1) {
		if m != prev {
			b.WriteString(m)
		}
		prev = m
	}
	return b.String()
}

func urlAllowed(u string, sel []domain.Chunk) bool {
	bare := utils.StripFragment(u)
	for _, c := range sel {
		if u == c.URL || bare == utils.StripFragment(c.URL) || bare == c.Page || u == c.Page || strings.Contains(c.Text, u) {
			return true
		}
	}
	return utils.IsTrustedHost(u)
}

func knownIdentifier(id string, identifiers map[string]struct{}, sel []domain.Chunk) bool {
	if _, ok := identifiers[id]; ok {
		return true
	}
	if _, ok := identifiers[strings.ToLower(id)]; ok {
		return true
	}
	for _, c := range sel {
		if strings.Contains(c.Text, id) {
			return true
		}
	}
	return false
}
