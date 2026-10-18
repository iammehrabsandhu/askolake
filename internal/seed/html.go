// Package seed builds the index bundle from docs, repositories, error
// messages, external pages and GitHub issues.
package seed

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// Selector constants, verified against the live olake.io Docusaurus HTML
// (see testdata/postgres.html and testdata/docs_index.html).
const (
	selDocRootPrimary  = "article .theme-doc-markdown"
	selDocRootFallback = ".theme-doc-markdown"
	selTabsContainer   = "tabs-container" // class
	selTabLabels       = `ul[role="tablist"] > li[role="tab"]`
	selTabPanel        = `[role="tabpanel"]`
	selTabList         = `ul[role="tablist"]`
	selAdmonHeading    = `[class*="admonitionHeading"]`
	selAdmonContent    = `[class*="admonitionContent"]`
	selDocRemove       = `.hash-link, nav, .theme-doc-toc-mobile, .theme-doc-footer, .pagination-nav, .theme-doc-breadcrumbs, .theme-edit-this-page, .theme-last-updated, script, style, svg, button, [class*="copyButton"], [class*="buttonGroup"]`
	selGenericRemove   = `nav, header, footer, aside, script, style, svg, form, button, .headerlink`
)

// genericH1 lists page h1 texts too generic to identify a page; the <title>
// is used instead (all connector pages on olake.io use "Overview").
var genericH1 = map[string]bool{"overview": true, "introduction": true, "release notes": true}

// Section is a heading-delimited part of a page.
type Section struct {
	Heading []string // page title, h2, h3
	Anchor  string
	Blocks  []Block
}

// Block is one content element of a section.
type Block struct {
	Kind string // "p","list","code","table","note"
	Text string
}

var (
	wsRe        = regexp.MustCompile(`[ \t\r\n\f]+`)
	spaceNLRe   = regexp.MustCompile(` *\n *`)
	multiSpRe   = regexp.MustCompile(` {2,}`)
	langClassRe = regexp.MustCompile(`(?:^|\s)language-([A-Za-z0-9_+-]+)`)
	admonTypeRe = regexp.MustCompile(`theme-admonition-([a-z]+)`)
	tabIdxRe    = regexp.MustCompile(`(?:^|\s)olake-tab-(\d+)(?:\s|$)`)
	zeroWidth   = strings.NewReplacer("\u200b", "", "\u200c", "", "\u200d", "", "\ufeff", "")
)

var inlineTags = map[string]bool{
	"#text": true, "a": true, "span": true, "code": true, "strong": true, "b": true, "em": true, "i": true,
	"small": true, "sup": true, "sub": true, "br": true, "kbd": true, "mark": true, "abbr": true, "s": true,
	"u": true, "del": true, "ins": true, "q": true, "cite": true, "time": true, "var": true, "samp": true,
	"label": true, "img": true,
}

var skipTags = map[string]bool{"img": true, "hr": true, "script": true, "style": true, "svg": true,
	"button": true, "input": true, "select": true, "textarea": true, "iframe": true, "noscript": true,
	"video": true, "audio": true, "picture": true, "source": true, "#comment": true}

type walker struct {
	title      string
	docusaurus bool
	h2         string
	sections   []Section
	prefix     string // pending "[Tab: x] " prefix for the next block
}

// ExtractDocusaurus extracts sections from an olake.io docs page.
func ExtractDocusaurus(pageURL string, html []byte) (string, []Section, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(html))
	if err != nil {
		return "", nil, fmt.Errorf("parse %s: %w", pageURL, err)
	}
	root := firstOf(doc, selDocRootPrimary, selDocRootFallback, "article", "main")
	if root == nil {
		return "", nil, fmt.Errorf("%s: no content root found", pageURL)
	}
	title := docTitle(doc)
	root.Find(selDocRemove).Remove()
	w := &walker{title: title, docusaurus: true}
	w.walk(root)
	return title, w.sections, nil
}

// ExtractGeneric extracts sections from an arbitrary documentation page.
func ExtractGeneric(pageURL string, html []byte) (string, []Section, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(html))
	if err != nil {
		return "", nil, fmt.Errorf("parse %s: %w", pageURL, err)
	}
	root := firstOf(doc, "main", "article", `[role="main"]`, "body")
	if root == nil {
		return "", nil, fmt.Errorf("%s: no content root found", pageURL)
	}
	title := ""
	if h1 := root.Find("h1").First(); h1.Length() > 0 {
		title = textOf(h1)
	}
	if title == "" {
		title = cutTitle(doc.Find("title").First().Text())
	}
	root.Find(selGenericRemove).Remove()
	w := &walker{title: title}
	w.walk(root)
	return title, w.sections, nil
}

func firstOf(doc *goquery.Document, sels ...string) *goquery.Selection {
	for _, s := range sels {
		if m := doc.Find(s).First(); m.Length() > 0 {
			return m
		}
	}
	return nil
}

func cutTitle(t string) string {
	t = strings.TrimSpace(wsRe.ReplaceAllString(t, " "))
	if i := strings.Index(t, " | "); i >= 0 {
		t = t[:i]
	}
	return strings.TrimSpace(t)
}

func docTitle(doc *goquery.Document) string {
	h1 := ""
	if h := doc.Find("article h1").First(); h.Length() > 0 {
		h1 = textOf(h)
	}
	page := cutTitle(doc.Find("title").First().Text())
	low := strings.ToLower(h1)
	switch {
	case h1 == "":
		return page
	case page == "":
		return h1
	case genericH1[low]:
		return page
	case len(strings.Fields(h1)) == 1:
		if strings.Contains(strings.ToLower(page), low) {
			return page
		}
		return page + " (" + h1 + ")"
	}
	return h1
}

func (w *walker) current() *Section {
	if len(w.sections) == 0 {
		w.sections = append(w.sections, Section{Heading: []string{w.title}})
	}
	return &w.sections[len(w.sections)-1]
}

func (w *walker) emit(kind, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if w.prefix != "" {
		if kind == "code" {
			text = strings.TrimSpace(w.prefix) + "\n" + text
		} else {
			text = w.prefix + text
		}
		w.prefix = ""
	}
	s := w.current()
	s.Blocks = append(s.Blocks, Block{Kind: kind, Text: text})
}

func (w *walker) startSection(heading []string, anchor string) {
	w.sections = append(w.sections, Section{Heading: heading, Anchor: anchor})
}

func headingAnchor(h *goquery.Selection) string {
	if id, ok := h.Attr("id"); ok && id != "" {
		return id
	}
	if a := h.Find("a[id]").First(); a.Length() > 0 {
		return a.AttrOr("id", "")
	}
	if a := h.Find("a[name]").First(); a.Length() > 0 {
		return a.AttrOr("name", "")
	}
	return ""
}

// walk emits blocks for the children of sel in document order. Runs of
// inline content between block elements become paragraphs.
func (w *walker) walk(sel *goquery.Selection) {
	var inline []*goquery.Selection
	flush := func() {
		if len(inline) == 0 {
			return
		}
		var b strings.Builder
		for _, n := range inline {
			renderNode(n, &b)
		}
		inline = nil
		w.emit("p", cleanText(b.String()))
	}
	sel.Contents().Each(func(_ int, c *goquery.Selection) {
		name := goquery.NodeName(c)
		if inlineTags[name] {
			inline = append(inline, c)
			return
		}
		flush()
		if skipTags[name] {
			return
		}
		w.element(name, c)
	})
	flush()
}

func (w *walker) element(name string, c *goquery.Selection) {
	if w.docusaurus {
		if c.HasClass(selTabsContainer) {
			w.tabs(c)
			return
		}
		if isAdmonition(c) {
			w.emit("note", admonitionText(c))
			return
		}
	}
	switch name {
	case "h1":
		// title already read
	case "h2":
		text := textOf(c)
		w.h2 = text
		w.startSection([]string{w.title, text}, headingAnchor(c))
	case "h3":
		h := []string{w.title}
		if w.h2 != "" {
			h = append(h, w.h2)
		}
		w.startSection(append(h, textOf(c)), headingAnchor(c))
	case "h4", "h5", "h6":
		if t := textOf(c); t != "" {
			w.emit("p", "**"+t+"**")
		}
	case "p", "blockquote", "summary", "dt", "figcaption", "caption":
		w.emit("p", textOf(c))
	case "ul", "ol":
		w.emit("list", listText(c, 0))
	case "pre":
		w.emit("code", codeText(c))
	case "table":
		w.emit("table", tableText(c))
	default:
		w.walk(c)
	}
}

func isAdmonition(c *goquery.Selection) bool {
	cls := c.AttrOr("class", "")
	if strings.Contains(cls, "theme-admonition") {
		return true
	}
	for _, f := range strings.Fields(cls) {
		if strings.HasPrefix(f, "admonition") && !strings.HasPrefix(f, "admonitionHeading") &&
			!strings.HasPrefix(f, "admonitionContent") && !strings.HasPrefix(f, "admonitionIcon") {
			return true
		}
	}
	return false
}

func admonitionText(c *goquery.Selection) string {
	typ := "NOTE"
	if m := admonTypeRe.FindStringSubmatch(c.AttrOr("class", "")); m != nil {
		typ = strings.ToUpper(m[1])
	}
	title, body := "", ""
	if h := c.Find(selAdmonHeading).First(); h.Length() > 0 {
		title = textOf(h)
	}
	if b := c.Find(selAdmonContent).First(); b.Length() > 0 {
		body = blockText(b)
	} else {
		body = blockText(c)
	}
	if title == "" {
		return "[" + typ + "] " + body
	}
	return "[" + typ + "] " + title + ": " + body
}

// blockText flattens nested block content into one line per block.
func blockText(c *goquery.Selection) string {
	inner := &walker{title: ""}
	inner.walk(c)
	var parts []string
	for _, s := range inner.sections {
		for _, b := range s.Blocks {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func (w *walker) tabs(c *goquery.Selection) {
	var labels []string
	c.Find(selTabLabels).Each(func(_ int, li *goquery.Selection) {
		labels = append(labels, strings.TrimSpace(textOf(li)))
	})
	c.Find(selTabList).Remove()
	pos := 0
	c.Find(selTabPanel).Each(func(_ int, p *goquery.Selection) {
		if p.ParentsFiltered("."+selTabsContainer).First().Get(0) != c.Get(0) {
			return // belongs to a nested tabs container
		}
		label := ""
		if m := tabIdxRe.FindStringSubmatch(p.AttrOr("class", "")); m != nil {
			var n int
			fmt.Sscanf(m[1], "%d", &n)
			if n < len(labels) {
				label = labels[n]
			}
		}
		if label == "" && pos < len(labels) {
			label = labels[pos]
		}
		pos++
		if label != "" {
			w.prefix = "[Tab: " + label + "] "
		}
		w.walk(p)
		w.prefix = ""
	})
}

func renderNode(c *goquery.Selection, b *strings.Builder) {
	switch name := goquery.NodeName(c); name {
	case "#text":
		b.WriteString(wsRe.ReplaceAllString(c.Text(), " "))
	case "br":
		b.WriteString("\n")
	case "code":
		if t := strings.TrimSpace(wsRe.ReplaceAllString(c.Text(), " ")); t != "" {
			b.WriteString("`" + t + "`")
		}
	default:
		if skipTags[name] {
			return
		}
		c.Contents().Each(func(_ int, cc *goquery.Selection) { renderNode(cc, b) })
	}
}

func cleanText(s string) string {
	s = zeroWidth.Replace(s)
	s = spaceNLRe.ReplaceAllString(s, "\n")
	s = multiSpRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// textOf renders an element's text: whitespace collapsed, inline code
// backticked, links as text, <br> as newline.
func textOf(c *goquery.Selection) string {
	var b strings.Builder
	c.Contents().Each(func(_ int, cc *goquery.Selection) { renderNode(cc, &b) })
	return cleanText(b.String())
}

func listText(list *goquery.Selection, depth int) string {
	ordered := goquery.NodeName(list) == "ol"
	indent := strings.Repeat("  ", depth)
	var lines []string
	n := 0
	list.ChildrenFiltered("li").Each(func(_ int, li *goquery.Selection) {
		n++
		clone := li.Clone()
		clone.Find("ul, ol").Remove()
		marker := "- "
		if ordered {
			marker = fmt.Sprintf("%d. ", n)
		}
		text := strings.ReplaceAll(textOf(clone), "\n", " ")
		if text != "" {
			lines = append(lines, indent+marker+text)
		}
		li.Find("ul, ol").Each(func(_ int, sub *goquery.Selection) {
			if sub.ParentsFiltered("li").First().Get(0) != li.Get(0) {
				return // deeper nesting handled recursively
			}
			if t := listText(sub, depth+1); t != "" {
				lines = append(lines, t)
			}
		})
	})
	return strings.Join(lines, "\n")
}

func codeText(pre *goquery.Selection) string {
	var code string
	if lines := pre.Find(".token-line"); lines.Length() > 0 {
		var ls []string
		lines.Each(func(_ int, l *goquery.Selection) {
			ls = append(ls, strings.TrimRight(zeroWidth.Replace(l.Text()), " \t\r\n"))
		})
		code = strings.Join(ls, "\n")
	} else {
		code = strings.Trim(zeroWidth.Replace(pre.Text()), "\n")
	}
	if strings.TrimSpace(code) == "" {
		return ""
	}
	lang := ""
	if m := langClassRe.FindStringSubmatch(pre.AttrOr("class", "")); m != nil {
		lang = m[1]
	} else if m := langClassRe.FindStringSubmatch(pre.Find("code").First().AttrOr("class", "")); m != nil {
		lang = m[1]
	}
	return "```" + lang + "\n" + code + "\n```"
}

func tableText(t *goquery.Selection) string {
	var rows []string
	t.Find("tr").Each(func(_ int, tr *goquery.Selection) {
		var cells []string
		tr.ChildrenFiltered("th, td").Each(func(_ int, td *goquery.Selection) {
			cells = append(cells, strings.ReplaceAll(textOf(td), "\n", " "))
		})
		if len(cells) > 0 {
			rows = append(rows, strings.Join(cells, " | "))
		}
	})
	return strings.Join(rows, "\n")
}

// SectionURL returns pageURL with #anchor when anchor is set.
func SectionURL(pageURL, anchor string) string {
	if anchor == "" {
		return pageURL
	}
	return pageURL + "#" + anchor
}
