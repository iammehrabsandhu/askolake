package data

import (
	"testing"
	"testing/fstest"
)

func TestLoadEmbedded(t *testing.T) {
	d, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Sources.Repos) != 4 || d.Sources.Repos[2].ValuesYAML[0] != "helm/olake/values.yaml" {
		t.Fatalf("repos: %+v", d.Sources.Repos)
	}
	if len(d.Sources.External) != 10 || d.Sources.External[0].Product != "" || len(d.Sources.LowValue) != 3 || len(d.Sources.Overview.Pages["olake"]) != 3 || len(d.Sources.Overview.Terms) == 0 {
		t.Fatalf("external: %+v", d.Sources.External)
	}
	if len(d.Synonyms) != 27 || d.Products.Default != "olake" || len(d.Products.QueryTerms["fusion"]) == 0 {
		t.Fatalf("synonyms/products: %d %+v", len(d.Synonyms), d.Products)
	}
	if len(d.Deprecated) != 5 || d.Deprecated[0].Term != "wal2json" {
		t.Fatalf("deprecated: %+v", d.Deprecated)
	}
}

func TestValidationRejectsRelativeURL(t *testing.T) {
	fsys := fstest.MapFS{
		"sources.yaml":    {Data: []byte("docs: {sitemap: /sitemap.xml}\ncommunity: {slack: https://a/, issues: https://b/}\n")},
		"synonyms.yaml":   {Data: []byte("groups: [[a, b]]\n")},
		"products.yaml":   {Data: []byte("default: olake\n")},
		"deprecated.yaml": {Data: []byte("[]\n")},
	}
	if _, err := LoadFS(fsys); err == nil {
		t.Fatal("relative sitemap URL accepted")
	}
}
