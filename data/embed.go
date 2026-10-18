// Package data embeds the content lists (data/*.yaml).
package data

import "embed"

// Files holds sources.yaml, synonyms.yaml, products.yaml and deprecated.yaml.
//
//go:embed *.yaml
var Files embed.FS
