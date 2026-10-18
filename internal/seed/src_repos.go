package seed

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"gopkg.in/yaml.v3"

	"olake-ask/internal/data"
	"olake-ask/internal/domain"
)

// Checkout is a fetched repository.
type Checkout struct {
	Repo data.Repo
	Dir  string
	SHA  string
}

var topKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*:`)

// RepoDocs extracts Markdown files and values.yaml sections from a checkout.
// It also returns Helm value key paths as identifiers.
func RepoDocs(co Checkout) ([]domain.Chunk, []string, error) {
	files, err := matchFiles(co.Dir, co.Repo.Markdown, co.Repo.Exclude)
	if err != nil {
		return nil, nil, fmt.Errorf("repo %s: %w", co.Repo.Name, err)
	}
	var chunks []domain.Chunk
	for _, rel := range files {
		b, err := os.ReadFile(filepath.Join(co.Dir, filepath.FromSlash(rel)))
		if err != nil {
			slog.Warn("repo: read failed", "repo", co.Repo.Name, "file", rel, "err", err)
			continue
		}
		title, sections := ExtractMarkdown(string(b))
		if title == "" {
			title = rel
		}
		chunks = append(chunks, Chunk(WithTitle(title, sections), Meta{
			PageURL:     BlobURL(co.Repo.URL, co.SHA, rel),
			TitlePrefix: co.Repo.Name + ": ",
			Source:      domain.SourceRepo,
			Product:     co.Repo.Product,
		})...)
	}
	var idents []string
	for _, rel := range co.Repo.ValuesYAML {
		b, err := os.ReadFile(filepath.Join(co.Dir, filepath.FromSlash(rel)))
		if err != nil {
			slog.Warn("repo: values file missing", "repo", co.Repo.Name, "file", rel, "err", err)
			continue
		}
		chunks = append(chunks, ValuesChunks(co, rel, string(b))...)
		idents = append(idents, YAMLKeyPaths(b)...)
	}
	return chunks, idents, nil
}

// matchFiles returns slash-separated repo-relative paths matching any include
// pattern and no exclude pattern, in walk (lexical) order.
func matchFiles(root string, include, exclude []string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			if rel != "." && matchAny(exclude, rel+"/x") {
				return filepath.SkipDir
			}
			return nil
		}
		if matchAny(include, rel) && !matchAny(exclude, rel) {
			out = append(out, rel)
		}
		return nil
	})
	return out, err
}

func matchAny(patterns []string, path string) bool {
	for _, p := range patterns {
		if ok, _ := doublestar.Match(p, path); ok {
			return true
		}
	}
	return false
}

// ValuesChunks splits a Helm values file at top-level keys; each section
// includes the comment lines directly above its key.
func ValuesChunks(co Checkout, rel, content string) []domain.Chunk {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	type sec struct {
		key   string
		start int // 0-based first line (including leading comments)
	}
	var secs []sec
	for i, l := range lines {
		if !topKeyRe.MatchString(l) {
			continue
		}
		start := i
		for start > 0 && strings.HasPrefix(strings.TrimSpace(lines[start-1]), "#") {
			start--
		}
		secs = append(secs, sec{key: strings.SplitN(l, ":", 2)[0], start: start})
	}
	if len(secs) == 0 {
		return nil
	}
	secs[0].start = 0 // any file header belongs to the first section
	var out []domain.Chunk
	for i, s := range secs {
		end := len(lines)
		if i+1 < len(secs) {
			end = secs[i+1].start
		}
		body := strings.TrimRight(strings.Join(lines[s.start:end], "\n"), "\n ")
		if strings.TrimSpace(body) == "" {
			continue
		}
		sections := []Section{{
			Heading: []string{rel, s.key},
			Anchor:  fmt.Sprintf("L%d", s.start+1),
			Blocks:  []Block{{Kind: "code", Text: "```yaml\n" + body + "\n```"}},
		}}
		out = append(out, Chunk(sections, Meta{
			PageURL:     BlobURL(co.Repo.URL, co.SHA, rel),
			TitlePrefix: co.Repo.Name + ": ",
			Source:      domain.SourceRepo,
			Product:     co.Repo.Product,
		})...)
	}
	return out
}

// YAMLKeyPaths returns every mapping key path (a, a.b, a.b.c) in the document.
func YAMLKeyPaths(b []byte) []string {
	var root yaml.Node
	if err := yaml.Unmarshal(b, &root); err != nil {
		slog.Warn("repo: values file is not valid YAML", "err", err)
		return nil
	}
	var out []string
	var walk func(n *yaml.Node, prefix string)
	walk = func(n *yaml.Node, prefix string) {
		switch n.Kind {
		case yaml.DocumentNode, yaml.SequenceNode:
			for _, c := range n.Content {
				walk(c, prefix)
			}
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				k := n.Content[i].Value
				if k == "" {
					continue
				}
				p := k
				if prefix != "" {
					p = prefix + "." + k
				}
				out = append(out, p)
				walk(n.Content[i+1], p)
			}
		}
	}
	walk(&root, "")
	return out
}
