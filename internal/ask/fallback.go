// Package ask implements the question-answering pipeline.
package ask

import (
	"fmt"
	"regexp"
	"strings"

	"olake-ask/internal/data"
	"olake-ask/internal/domain"
	"olake-ask/internal/tuning"
	"olake-ask/internal/utils"
)

const (
	dontKnowText   = "I don't know. I couldn't find this in the OLake documentation or repositories."
	searchOnlyText = "I can't generate a full answer right now, but these sections look most relevant:"
	refusedText    = "I can only help with questions about OLake and OLake Fusion: setup, sources, sync modes, catalogs, table maintenance, deployment and troubleshooting."
	tooShortText   = "Please ask a complete question about OLake or OLake Fusion."
)

var fenceMarkRe = regexp.MustCompile("```[A-Za-z0-9_+.-]*")

// DontKnow is the answer when the content does not support an answer.
func DontKnow(c data.Community, suggestions []string) domain.Result {
	text := dontKnowText
	if len(suggestions) > 0 {
		text += "\n\nThese pages may be related:\n" + strings.Join(suggestions, "\n")
	}
	text += fmt.Sprintf("\n\nYou can ask the OLake community on Slack (%s) or open an issue at %s.", c.Slack, c.Issues)
	return domain.Result{Answer: text, Sources: []string{}, Mode: domain.ModeDontKnow}
}

// SearchOnly lists the most relevant sections when no LLM answer is possible.
func SearchOnly(c data.Community, hits []domain.Hit, chunk func(int) domain.Chunk) domain.Result {
	if len(hits) == 0 {
		return DontKnow(c, nil)
	}
	var b strings.Builder
	b.WriteString(searchOnlyText)
	sources := []string{}
	seen := map[string]bool{}
	for _, h := range hits {
		ch := chunk(h.ChunkID)
		if seen[ch.URL] {
			continue
		}
		seen[ch.URL] = true
		sources = append(sources, ch.URL)
		n := len(sources)
		snippet, _ := utils.TruncateWords(fenceMarkRe.ReplaceAllString(ch.Text, " "), tuning.SearchOnlySnippetWords)
		fmt.Fprintf(&b, "\n%d. %s — %s … [%d]", n, ch.Title, snippet, n)
	}
	return domain.Result{Answer: b.String(), Sources: sources, Mode: domain.ModeSearchOnly}
}

// Refused is the answer for off-limits input. reason "too_short" has its own text.
func Refused(reason string) domain.Result {
	text := refusedText
	if reason == "too_short" {
		text = tooShortText
	}
	return domain.Result{Answer: text, Sources: []string{}, Mode: domain.ModeRefused}
}

// suggestions lists the first unique pages of hits as "- <title>: <page>".
func suggestions(hits []domain.Hit, chunk func(int) domain.Chunk, n int) []string {
	var out []string
	seen := map[string]bool{}
	for _, h := range hits {
		c := chunk(h.ChunkID)
		if seen[c.Page] {
			continue
		}
		seen[c.Page] = true
		out = append(out, "- "+c.Title+": "+c.Page)
		if len(out) == n {
			break
		}
	}
	return out
}
