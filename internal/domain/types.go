// Package domain holds the core types shared by the seeder and the server.
package domain

import "time"

// SourceKind identifies where a chunk came from.
type SourceKind string

const (
	SourceDocs     SourceKind = "docs"
	SourceRepo     SourceKind = "repo"
	SourceCode     SourceKind = "code"
	SourceExternal SourceKind = "external"
	SourceIssue    SourceKind = "issue"
)

// SourceOrder is the stable order used when assigning chunk IDs.
var SourceOrder = []SourceKind{SourceDocs, SourceRepo, SourceCode, SourceExternal, SourceIssue}

// Product is the OLake product a chunk or query is about.
type Product string

const (
	ProductNone   Product = ""
	ProductOLake  Product = "olake"
	ProductFusion Product = "fusion"
)

// Chunk is one retrievable unit of content.
type Chunk struct {
	ID         int        `json:"id"`    // dense 0..n-1, assigned at write time
	URL        string     `json:"url"`   // absolute, with #anchor when available
	Page       string     `json:"page"`  // URL without fragment (per-page cap key)
	Title      string     `json:"title"` // "Page title › H2 › H3"
	Text       string     `json:"text"`  // body only (title not included)
	Source     SourceKind `json:"source"`
	Product    Product    `json:"product"`
	Deprecated bool       `json:"deprecated,omitempty"`
	LowValue   bool       `json:"low_value,omitempty"`   // generic text that matches many questions (sources.yaml low_value)
	ErrPattern string     `json:"err_pattern,omitempty"` // SourceCode error chunks only
}

// EmbedInfo describes the embedding model used for a bundle.
type EmbedInfo struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Dim      int    `json:"dim"`
	Pieces   int    `json:"pieces"`
	// OutputDim is the dimension requested from the API (0 = model default);
	// queries must request the same one.
	OutputDim int `json:"output_dim,omitempty"`
}

// Manifest describes an index bundle.
type Manifest struct {
	Schema      int                `json:"schema"`  // = 1
	Version     string             `json:"version"` // e.g. 20261017T1030Z-<8 hex of sha256(chunks file)>
	BuiltAt     time.Time          `json:"built_at"`
	Repos       map[string]string  `json:"repos"` // name → commit SHA
	Counts      map[SourceKind]int `json:"counts"`
	Chunks      int                `json:"chunks"`
	Identifiers []string           `json:"identifiers"` // extra known identifiers (json tags, helm keys)
	Embedder    *EmbedInfo         `json:"embedder"`    // nil when no vectors
	// APIEmbedder describes optional second vectors (vectors_api.bin) over the
	// same pieces as Embedder; nil when absent. Older bundles never have it.
	APIEmbedder *EmbedInfo `json:"api_embedder,omitempty"`
}

// Term is a weighted query term.
type Term struct {
	Text   string
	Weight float64
}

// Query is a prepared user question.
type Query struct {
	Raw        string
	Content    []string // unique content tokens of Raw (no stopwords, no synonyms), max 64
	Terms      []Term   // Content (weight 1) + synonym expansions (weight tuning.SynonymWeight)
	Product    Product  // detected from products.yaml query_terms; ProductNone if none or both
	ErrorLike  bool
	NonEnglish bool
	Mentions   string // lowercased Raw (used by deprecated logic)
}

// Hit is one search result.
type Hit struct {
	ChunkID  int
	Score    float64
	Coverage float64
	Cosine   float64
	// CosineFloor is the relevance floor of the model that produced Cosine
	// (0 = tuning.FloorCosine). Set by vector search only.
	CosineFloor float64
}

// Mode is how a question was handled.
type Mode string

const (
	ModeAnswered   Mode = "answered"
	ModeDontKnow   Mode = "dont_know"
	ModeSearchOnly Mode = "search_only"
	ModeRefused    Mode = "refused"
)

// Result is the outcome of answering a question.
type Result struct {
	Answer   string
	Sources  []string
	Mode     Mode
	LLMCalls int
	InTok    int
	OutTok   int
	Cached   bool
}
