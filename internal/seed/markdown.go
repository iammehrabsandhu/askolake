package seed

import (
	"fmt"
	"regexp"
	"strings"

	"olake-ask/internal/utils"
)

var (
	htmlCommentRe = regexp.MustCompile(`(?s)<!--.*?-->`)
	shortcodeRe   = regexp.MustCompile(`\{\{[<%].*?[%>]\}\}`)
	mdImageRe     = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	htmlTagRe     = regexp.MustCompile(`</?[A-Za-z][A-Za-z0-9-]*(?:\s[^<>]*)?/?>`)
	fenceOpenRe   = regexp.MustCompile("^\\s*(```+|~~~+)\\s*([A-Za-z0-9_+.-]*)")
	mdHeadingRe   = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	mdListRe      = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+`)
	mdTableSepRe  = regexp.MustCompile(`^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$`)
	fmTitleRe     = regexp.MustCompile(`(?m)^title\s*[:=]\s*(.+?)\s*$`)
	mdInlineFmtRe = regexp.MustCompile("[`*_]")
	mdLinkRe      = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
)

// ExtractMarkdown parses a Markdown document into a title and sections.
// The title is "" when the document has none (callers fall back to the path).
func ExtractMarkdown(md string) (string, []Section) {
	md = strings.ReplaceAll(md, "\r\n", "\n")
	title := ""
	md, title = stripFrontMatter(md)
	md = htmlCommentRe.ReplaceAllString(md, "")

	var (
		sections []Section
		cur      *Section
		h2       string
		para     []string
		kind     string // current multi-line block kind: p, list, table
		inFence  bool
		fence    string
		fenceBuf []string
		slugs    = map[string]int{}
	)
	section := func() *Section {
		if cur == nil {
			sections = append(sections, Section{})
			cur = &sections[len(sections)-1]
		}
		return cur
	}
	flush := func() {
		if len(para) == 0 {
			return
		}
		text := strings.Join(para, "\n")
		if kind == "p" {
			text = strings.Join(strings.Fields(text), " ")
		}
		if strings.TrimSpace(text) != "" {
			s := section()
			s.Blocks = append(s.Blocks, Block{Kind: kind, Text: text})
		}
		para, kind = nil, ""
	}
	anchor := func(h string) string {
		slug := utils.GitHubSlug(plainHeading(h))
		n := slugs[slug]
		slugs[slug] = n + 1
		if n > 0 {
			return fmt.Sprintf("%s-%d", slug, n)
		}
		return slug
	}

	for _, line := range strings.Split(md, "\n") {
		if inFence {
			if strings.HasPrefix(strings.TrimSpace(line), fence) {
				s := section()
				s.Blocks = append(s.Blocks, Block{Kind: "code", Text: strings.Join(fenceBuf, "\n") + "\n```"})
				inFence, fenceBuf = false, nil
				continue
			}
			fenceBuf = append(fenceBuf, line)
			continue
		}
		if m := fenceOpenRe.FindStringSubmatch(line); m != nil {
			flush()
			inFence, fence = true, m[1][:3]
			fenceBuf = []string{"```" + m[2]}
			continue
		}
		line = shortcodeRe.ReplaceAllString(line, "")
		line = mdImageRe.ReplaceAllString(line, "")
		line = htmlTagRe.ReplaceAllString(line, "")
		trimmed := strings.TrimSpace(line)
		if m := mdHeadingRe.FindStringSubmatch(trimmed); m != nil {
			flush()
			level, text := len(m[1]), strings.TrimSpace(m[2])
			a := anchor(text)
			switch {
			case level == 1 && title == "":
				title = plainHeading(text)
			case level <= 2:
				h2 = plainHeading(text)
				sections = append(sections, Section{Heading: []string{h2}, Anchor: a})
				cur = &sections[len(sections)-1]
			case level == 3:
				h := []string{}
				if h2 != "" {
					h = append(h, h2)
				}
				sections = append(sections, Section{Heading: append(h, plainHeading(text)), Anchor: a})
				cur = &sections[len(sections)-1]
			default:
				s := section()
				s.Blocks = append(s.Blocks, Block{Kind: "p", Text: "**" + plainHeading(text) + "**"})
			}
			continue
		}
		if trimmed == "" {
			flush()
			continue
		}
		var k string
		switch {
		case strings.HasPrefix(trimmed, "|"):
			k = "table"
			if mdTableSepRe.MatchString(trimmed) {
				continue
			}
		case mdListRe.MatchString(line):
			k = "list"
		case kind == "list" && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")):
			k = "list" // continuation line
		default:
			k = "p"
			if kind == "list" {
				k = "list" // lazy continuation
			}
		}
		if kind != "" && k != kind {
			flush()
		}
		kind = k
		if k == "table" {
			trimmed = strings.Trim(trimmed, "| ")
			cells := strings.Split(trimmed, "|")
			for i := range cells {
				cells[i] = strings.TrimSpace(cells[i])
			}
			para = append(para, strings.Join(cells, " | "))
		} else if k == "list" {
			para = append(para, strings.TrimRight(line, " \t"))
		} else {
			para = append(para, trimmed)
		}
	}
	if inFence && len(fenceBuf) > 0 {
		s := section()
		s.Blocks = append(s.Blocks, Block{Kind: "code", Text: strings.Join(fenceBuf, "\n") + "\n```"})
	}
	flush()
	return title, sections
}

// WithTitle prefixes every section heading with the page title.
func WithTitle(title string, sections []Section) []Section {
	for i := range sections {
		sections[i].Heading = append([]string{title}, sections[i].Heading...)
	}
	return sections
}

func plainHeading(h string) string {
	h = mdLinkRe.ReplaceAllString(h, "$1")
	return strings.TrimSpace(mdInlineFmtRe.ReplaceAllString(h, ""))
}

func stripFrontMatter(md string) (string, string) {
	lines := strings.Split(md, "\n")
	if len(lines) == 0 {
		return md, ""
	}
	delim := strings.TrimSpace(lines[0])
	if delim != "---" && delim != "+++" {
		return md, ""
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != delim {
			continue
		}
		title := ""
		if m := fmTitleRe.FindStringSubmatch(strings.Join(lines[1:i], "\n")); m != nil {
			title = strings.Trim(m[1], `"'`)
		}
		return strings.Join(lines[i+1:], "\n"), title
	}
	return md, ""
}
