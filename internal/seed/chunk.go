package seed

import (
	"strings"
	"unicode"

	"olake-ask/internal/domain"
	"olake-ask/internal/tuning"
	"olake-ask/internal/utils"
)

// Meta describes where a page's chunks come from.
type Meta struct {
	PageURL     string // URL without fragment
	TitlePrefix string // e.g. "olake: " or "iceberg.apache.org: "
	Source      domain.SourceKind
	Product     domain.Product
}

// Chunk turns sections into word-bounded chunks.
func Chunk(sections []Section, meta Meta) []domain.Chunk {
	var out []domain.Chunk
	for _, s := range sections {
		url := SectionURL(meta.PageURL, s.Anchor)
		title := meta.TitlePrefix + strings.Join(nonEmpty(s.Heading), " › ")
		for _, text := range chunkBlocks(s.Blocks) {
			if len(utils.Words(text)) < tuning.ChunkMinWords {
				continue
			}
			out = append(out, domain.Chunk{
				URL: url, Page: utils.StripFragment(url), Title: title, Text: text,
				Source: meta.Source, Product: meta.Product,
			})
		}
	}
	return out
}

func nonEmpty(ss []string) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func wc(s string) int { return len(utils.Words(s)) }

// chunkBlocks packs blocks into texts of at most ChunkMaxWords words (plus at
// most ChunkMergeWords when a short chunk joins the next block); a new chunk
// carries the last ChunkOverlapWords words of plain text as overlap.
func chunkBlocks(blocks []Block) []string {
	var (
		out       []string
		cur       []string
		curWords  int
		curPlain  []string
		carryOnly bool // cur holds only the overlap prefix
	)
	flush := func() {
		if len(cur) > 0 && !carryOnly {
			out = append(out, strings.Join(cur, "\n\n"))
		}
		cur, curWords, curPlain, carryOnly = nil, 0, nil, false
	}
	for _, b := range blocks {
		for _, p := range splitBlock(b) {
			w := wc(p.Text)
			// A short chunk (e.g. "The table below shows…") joins the next
			// block instead of standing alone, even if that exceeds the max.
			if curWords >= tuning.ChunkMergeWords && curWords+w > tuning.ChunkMaxWords {
				tail := lastWords(strings.Join(curPlain, " "), tuning.ChunkOverlapWords)
				flush()
				if tail != "" && wc(tail)+w <= tuning.ChunkMaxWords {
					cur, curWords, carryOnly = []string{tail}, wc(tail), true
				}
			}
			cur = append(cur, p.Text)
			curWords += w
			carryOnly = false
			if p.Kind != "code" {
				curPlain = append(curPlain, p.Text)
			}
		}
	}
	flush()
	return out
}

func lastWords(s string, n int) string {
	w := utils.Words(s)
	if len(w) > n {
		w = w[len(w)-n:]
	}
	return strings.Join(w, " ")
}

// splitBlock splits a block larger than ChunkMaxWords: code by lines
// (re-fenced), anything else by words with overlap.
func splitBlock(b Block) []Block {
	if wc(b.Text) <= tuning.ChunkMaxWords {
		return []Block{b}
	}
	if b.Kind == "code" {
		return splitCode(b)
	}
	words := utils.Words(b.Text)
	step := tuning.ChunkMaxWords - tuning.ChunkOverlapWords
	var out []Block
	for start := 0; start < len(words); start += step {
		end := min(start+tuning.ChunkMaxWords, len(words))
		out = append(out, Block{Kind: b.Kind, Text: strings.Join(words[start:end], " ")})
		if end == len(words) {
			break
		}
	}
	return out
}

func splitCode(b Block) []Block {
	isFence := func(l string) bool { return strings.HasPrefix(strings.TrimSpace(l), "```") }
	body := strings.Split(b.Text, "\n")
	prefix := "" // e.g. a "[Tab: X]" line before the fence
	if len(body) > 1 && !isFence(body[0]) && isFence(body[1]) {
		prefix, body = body[0], body[1:]
	}
	open := "```"
	if len(body) > 0 && isFence(body[0]) {
		open, body = strings.TrimSpace(body[0]), body[1:]
	}
	if n := len(body); n > 0 && strings.TrimSpace(body[n-1]) == "```" {
		body = body[:n-1]
	}
	budget := tuning.ChunkMaxWords - 2 - wc(prefix) // fence lines count as words
	var out []Block
	var part []string
	words := 0
	emit := func() {
		if len(part) == 0 {
			return
		}
		text := open + "\n" + strings.Join(part, "\n") + "\n```"
		if prefix != "" && len(out) == 0 {
			text = prefix + "\n" + text
		}
		out = append(out, Block{Kind: "code", Text: text})
		part, words = nil, 0
	}
	for _, l := range body {
		lw := wc(l)
		if lw > budget { // one enormous line: split it by words
			emit()
			ws := utils.Words(l)
			for s := 0; s < len(ws); s += budget {
				part = []string{strings.Join(ws[s:min(s+budget, len(ws))], " ")}
				emit()
			}
			continue
		}
		if words+lw > budget {
			emit()
		}
		part = append(part, l)
		words += lw
	}
	emit()
	return out
}

// embedWords returns the words of text without symbol-only tokens (table
// pipes, ✅, —): they carry no meaning and swamp a table's embedding.
func embedWords(text string) []string {
	ws := utils.Words(text)
	out := ws[:0:0]
	for _, w := range ws {
		if strings.IndexFunc(w, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0 {
			out = append(out, w)
		}
	}
	return out
}

// Pieces splits chunks into embedding windows of PieceWords words, each
// prefixed by the chunk title, and returns the owning chunk ID per piece.
func Pieces(chunks []domain.Chunk) ([]string, []int) {
	var texts []string
	var owner []int
	step := tuning.PieceWords - tuning.PieceOverlapWords
	for _, c := range chunks {
		words := embedWords(c.Text)
		if len(words) <= tuning.PieceWords {
			texts = append(texts, c.Title+"\n"+strings.Join(words, " "))
			owner = append(owner, c.ID)
			continue
		}
		for start := 0; start < len(words); start += step {
			end := min(start+tuning.PieceWords, len(words))
			texts = append(texts, c.Title+"\n"+strings.Join(words[start:end], " "))
			owner = append(owner, c.ID)
			if end == len(words) {
				break
			}
		}
	}
	return texts, owner
}
