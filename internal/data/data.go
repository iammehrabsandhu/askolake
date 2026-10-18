// Package data parses the embedded content lists (data/*.yaml) into typed structs.
package data

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	datafiles "olake-ask/data"
	"olake-ask/internal/domain"
	"olake-ask/internal/utils"
)

// Data is the parsed content of all data files.
type Data struct {
	Sources    Sources
	Synonyms   [][]string
	Products   Products
	Deprecated []Deprecated
}

// Sources is data/sources.yaml.
type Sources struct {
	Docs      Docs       `yaml:"docs"`
	Repos     []Repo     `yaml:"repos"`
	External  []External `yaml:"external"`
	Issues    Issues     `yaml:"issues"`
	Community Community  `yaml:"community"`
	// LowValue lists URL substrings of sections that match almost any
	// question but answer none (generic "about OLake" text); they are
	// down-ranked, not removed.
	LowValue []string `yaml:"low_value"`
}

// Docs configures the docs sitemap crawl.
type Docs struct {
	Sitemap           string   `yaml:"sitemap"`
	IncludePrefixes   []string `yaml:"include_prefixes"`
	ExcludeSubstrings []string `yaml:"exclude_substrings"`
}

// Repo is one GitHub repository to index.
type Repo struct {
	Name       string         `yaml:"name"`
	URL        string         `yaml:"url"`
	Ref        string         `yaml:"ref"`
	Product    domain.Product `yaml:"product"`
	Markdown   []string       `yaml:"markdown"`
	Exclude    []string       `yaml:"exclude"`
	ValuesYAML []string       `yaml:"values_yaml"`
	Errors     bool           `yaml:"errors"`
}

// External is one allowlisted external page. Source "docs" marks a
// first-party page (e.g. the OLake blog) that ranks like the docs.
type External struct {
	URL     string         `yaml:"url"`
	Product domain.Product `yaml:"product"`
	Source  string         `yaml:"source"` // "" (external) | docs
}

// Issues configures the GitHub issues source.
type Issues struct {
	Repos      []string `yaml:"repos"`
	MaxPerRepo int      `yaml:"max_per_repo"`
}

// Community holds links shown in fallback answers.
type Community struct {
	Slack  string `yaml:"slack"`
	Issues string `yaml:"issues"`
}

// Products is data/products.yaml.
type Products struct {
	URLRules   []URLRule                   `yaml:"url_rules"`
	Default    domain.Product              `yaml:"default"`
	QueryTerms map[domain.Product][]string `yaml:"query_terms"`
}

// URLRule maps a URL substring to a product.
type URLRule struct {
	Contains string         `yaml:"contains"`
	Product  domain.Product `yaml:"product"`
}

// Deprecated is one entry of data/deprecated.yaml.
type Deprecated struct {
	Term  string   `yaml:"term"`
	Note  string   `yaml:"note"`
	URL   string   `yaml:"url"`
	Pages []string `yaml:"pages"`
}

var (
	once   sync.Once
	cached Data
	errC   error
)

// Load parses the embedded data files once (the data is immutable).
func Load() (Data, error) {
	once.Do(func() { cached, errC = LoadFS(datafiles.Files) })
	return cached, errC
}

// LoadFS parses the four data files from fsys.
func LoadFS(fsys fs.FS) (Data, error) {
	var d Data
	if err := readYAML(fsys, "sources.yaml", &d.Sources); err != nil {
		return d, err
	}
	var syn struct {
		Groups [][]string `yaml:"groups"`
	}
	if err := readYAML(fsys, "synonyms.yaml", &syn); err != nil {
		return d, err
	}
	d.Synonyms = syn.Groups
	if err := readYAML(fsys, "products.yaml", &d.Products); err != nil {
		return d, err
	}
	if err := readYAML(fsys, "deprecated.yaml", &d.Deprecated); err != nil {
		return d, err
	}
	if err := d.validate(); err != nil {
		return d, fmt.Errorf("data files: %w", err)
	}
	return d, nil
}

func readYAML(fsys fs.FS, name string, out any) error {
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		return fmt.Errorf("read data/%s: %w", name, err)
	}
	if err := yaml.Unmarshal(b, out); err != nil {
		return fmt.Errorf("parse data/%s: %w", name, err)
	}
	return nil
}

func (d Data) validate() error {
	var errs []error
	url := func(where, u string) {
		if !utils.IsHTTPURL(u) {
			errs = append(errs, fmt.Errorf("%s: %q is not an absolute http(s) URL", where, u))
		}
	}
	nonEmpty := func(where, s string) {
		if strings.TrimSpace(s) == "" {
			errs = append(errs, fmt.Errorf("%s: empty value", where))
		}
	}
	s := d.Sources
	url("sources.docs.sitemap", s.Docs.Sitemap)
	for _, p := range s.Docs.IncludePrefixes {
		url("sources.docs.include_prefixes", p)
	}
	for _, x := range s.Docs.ExcludeSubstrings {
		nonEmpty("sources.docs.exclude_substrings", x)
	}
	for _, r := range s.Repos {
		nonEmpty("sources.repos.name", r.Name)
		nonEmpty("sources.repos["+r.Name+"].ref", r.Ref)
		url("sources.repos["+r.Name+"].url", r.URL)
		for _, p := range append(append([]string{}, r.Markdown...), r.Exclude...) {
			nonEmpty("sources.repos["+r.Name+"] pattern", p)
		}
	}
	for _, e := range s.External {
		url("sources.external", e.URL)
		if e.Source != "" && e.Source != "docs" {
			errs = append(errs, fmt.Errorf("sources.external[%s].source: %q (allowed: docs)", e.URL, e.Source))
		}
	}
	for _, r := range s.Issues.Repos {
		if strings.Count(r, "/") != 1 {
			errs = append(errs, fmt.Errorf("sources.issues.repos: %q is not owner/name", r))
		}
	}
	for _, x := range s.LowValue {
		nonEmpty("sources.low_value", x)
	}
	url("sources.community.slack", s.Community.Slack)
	url("sources.community.issues", s.Community.Issues)
	for i, g := range d.Synonyms {
		if len(g) < 2 {
			errs = append(errs, fmt.Errorf("synonyms.groups[%d]: needs at least 2 phrases", i))
		}
		for _, p := range g {
			nonEmpty(fmt.Sprintf("synonyms.groups[%d]", i), p)
		}
	}
	for _, r := range d.Products.URLRules {
		nonEmpty("products.url_rules.contains", r.Contains)
		nonEmpty("products.url_rules.product", string(r.Product))
	}
	nonEmpty("products.default", string(d.Products.Default))
	for p, terms := range d.Products.QueryTerms {
		for _, t := range terms {
			nonEmpty("products.query_terms."+string(p), t)
		}
	}
	for _, e := range d.Deprecated {
		nonEmpty("deprecated.term", e.Term)
		nonEmpty("deprecated["+e.Term+"].note", e.Note)
		url("deprecated["+e.Term+"].url", e.URL)
		for _, p := range e.Pages {
			nonEmpty("deprecated["+e.Term+"].pages", p)
		}
	}
	return errors.Join(errs...)
}
