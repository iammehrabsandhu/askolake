// Package tuning holds every numeric and tunable constant of the system.
package tuning

import (
	"time"

	"olake-ask/internal/domain"
)

const (
	MaxBodyBytes     = 16 << 10
	MaxQuestionChars = 2000
	MinQuestionChars = 3
	RatePerIPPerMin  = 1000000
	RateBurst        = 1000000
	GlobalRatePerMin = 1000000
	GlobalRateBurst  = 1000000
	CacheSize        = 1000
	CacheTTL         = 24 * time.Hour

	NonASCIIRatio    = 0.2
	ErrLiteralMinLen = 12

	IssueBodyChars   = 1500
	IssueReplyChars  = 1500
	IssueFenceLines  = 15
	ErrContextBefore = 6
	ErrContextAfter  = 3

	AnswerMaxTokens      = 700
	AuxMaxTokens         = 200
	Temperature          = 0.1
	TimeoutRewrite       = 4 * time.Second
	TimeoutRerank        = 5 * time.Second
	TimeoutAnswer        = 20 * time.Second
	TimeoutQueryEmbed    = 2 * time.Second
	TimeoutQueryEmbedAPI = 1500 * time.Millisecond // shorter: a failure falls back to local vectors
	RequestTimeout       = 25 * time.Second        // answers stay under 30 s (scoring limit); the LLM call gets TimeoutAnswer
	TimeoutModels        = 10 * time.Second
	LLMRetries           = 1
	MaxConcurrentLLM     = 16
	CrawlConcurrency     = 4
	CrawlDelay           = 250 * time.Millisecond
	FetchTimeout         = 20 * time.Second
	FetchRetries         = 1
	UnauthCommentCap     = 50
	MinDocsChunks        = 300
	MaxDocsPageFailRatio = 0.2
	MinPageWords         = 20
	MinRateRemaining     = 5
	GitHubMaxPages       = 10 // the issues endpoint rejects page-number pagination beyond this

	SearchOnlyResults      = 4
	SearchOnlySnippetWords = 30
	Suggestions            = 3
	RerankSnippetChars     = 500
	MaxRewriteLineChars    = 200

	EmbedAPIBatch      = 64
	CohereEmbedBatch   = 96   // Cohere v2 /embed accepts at most 96 texts per call
	CohereInputsPerMin = 1800 // Cohere allows 2,000 embed inputs/min; stay under it
	SeedEmbedBatch     = 256
	SelfCheckCov       = 0.5
	SelfCheckCos       = 0.2

	RetryBase     = 500 * time.Millisecond
	RetryJitter   = 250 * time.Millisecond
	RetryMax      = 3 * time.Second
	HTTPErrBody   = 512
	MaxFetchBytes = 10 << 20

	RateSweepEvery    = time.Minute
	RateIdleTTL       = 10 * time.Minute
	ShutdownGrace     = 10 * time.Second
	ModelLoadTimeout  = 60 * time.Second
	ReadHeaderTimeout = 10 * time.Second
	ReadTimeout       = 20 * time.Second
	WriteTimeout      = 60 * time.Second
	IdleTimeout       = 60 * time.Second
	LogQuestionMax    = 300
)

// Turnable knobs: vars (not consts) so the test harness (cmd/e2e) can
// override them with Set; production never calls Set and runs these
// defaults. Query-time knobs take effect immediately; seed-time knobs
// shape the bundle and need a reseed.
var (
	// Query time.
	Candidates           = 30
	TopK                 = 14
	RerankTopK           = 8
	RRFK                 = 60.0
	PerPageCap           = 3
	NearDupJaccard       = 1.0 // drop a hit whose token set overlaps a better hit this much (1.0 = identical sets only)
	TitleWeight          = 2.5
	BM25K1               = 1.2
	BM25B                = 0.3
	SynonymWeight        = 0.5
	MaxQueryTerms        = 64
	ProductBoost         = 1.3
	DeprecatedPenalty    = 0.5
	LowValuePenalty      = 0.3
	FloorCoverage        = 0.60 // calibrated on eval/: golden ≥ 0.70, off-topic ≤ 0.50
	FloorCosine          = 0.50 // calibrated on eval/ (model2vec BASE8M): golden ≥ 0.62, off-topic ≤ 0.43
	WeakCoverage         = 0.5
	ContextCharsPerChunk = 2400

	// Seed time (reseed to take effect).
	ChunkMaxWords     = 350
	ChunkOverlapWords = 40
	ChunkMinWords     = 8
	ChunkMergeWords   = 60 // a chunk shorter than this joins the next block instead of standing alone
	PieceWords        = 120
	PieceOverlapWords = 20
	ExternalMaxChunks = 60
)

// FloorCosineByModel is the calibrated FloorCosine per "provider/model".
// Models not listed use FloorCosine (and log a warning at startup);
// calibrate with make eval before relying on a new model.
var FloorCosineByModel = map[string]float64{
	"model2vec/BASE8M": FloorCosine,
}

// SourceWeight multiplies fused scores by source kind.
var SourceWeight = map[domain.SourceKind]float64{
	domain.SourceDocs:     1.0,
	domain.SourceRepo:     0.85,
	domain.SourceCode:     0.8,
	domain.SourceExternal: 0.7,
	domain.SourceIssue:    0.6,
}
