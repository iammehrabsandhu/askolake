# olake-ask

Retrieval-augmented question answering over the [OLake](https://olake.io/docs/) documentation and the
`datazip-inc` open-source repositories. A seeder builds a compact index bundle (docs, repo Markdown,
Helm values, Go error messages, allowlisted external pages, answered GitHub issues); a small Go server
with the bundle compiled in answers questions with cited sources, and a vanilla-JS widget embeds the
chat on any page.

```bash
curl -X POST https://<host>/ask -H 'Content-Type: application/json' \
  -d '{"question":"How do I enable CDC for Postgres?"}'
```
```json
{"answer": "1. Enable logical replication … [1]\n2. Create a publication … [2]",
 "sources": ["https://olake.io/docs/connectors/postgres/setup/generic/", "https://olake.io/docs/connectors/postgres/#2-provide-configuration-details"]}
```

The success body always has exactly the keys `answer` and `sources`; `sources` is always an array and
every `[n]` marker in `answer` points at `sources[n-1]`. The response header `X-Ask-Mode` tells how the
question was handled (see [fallback modes](#fallback-modes)); `X-Index-Version` names the bundle.

## Architecture

```
            ┌──────────── cmd/seeder (local or CI) ─────────────┐
 olake.io   │ sitemap crawl ─┐                                   │
 sitemap ──▶│ git fetch repos├─▶ sections ─▶ chunks ─▶ tag ─▶     │      assets/index/
 GitHub ───▶│ go/ast errors  │    dedupe ─▶ embed (Model2Vec) ─▶ ├──▶  manifest.json
 external ─▶│ issues         ┘    self-check ─▶ atomic write     │      chunks.jsonl.gz
            └────────────────────────────────────────────────────┘      vectors.bin, pieces.json
                                                                              │ go:embed
            ┌──────────────── cmd/server (Render, 512 MB) ─────────────────────▼───┐
 POST /ask ▶│ guards ─▶ cache ─▶ prepare query ─┬─ BM25F keyword ─┐                │
            │ (redact,  (LRU,     (tokens,      ├─ exact cosine ──┼─▶ RRF fusion   │
            │  injection singleflight) synonyms,├─ error regexes ─┘   + boosts     │
            │  checks)            product)      │                     + page cap   │
            │      ─▶ relevance floor ─▶ (optional rerank) ─▶ one LLM call         │
            │      ─▶ verification (citations, URLs, identifiers, deprecations)    │
            └──────────────────────────────────────────────────────────────────────┘
```

Request pipeline: guards → cache → prepare → keyword + vector + error search → RRF fusion with source
weights, product boost, deprecated penalty and a per-page cap → relevance floor → optional rerank →
**one** LLM call → verification → response.

### Fallback modes

| Mode | When | Response |
|---|---|---|
| `answered` | LLM answered from the passages and cited at least one of them | Answer with `[n]` markers + sources |
| `dont_know` | Retrieval below the relevance floor (no LLM call), LLM said not answerable, zero valid citations, or rerank confidence `none` | "I don't know" + up to 3 related pages (not for floor hits) + community links |
| `search_only` | No LLM configured, daily token budget exhausted, or the answer call failed (after fallback model) | The 4 most relevant sections with citations |
| `refused` | Prompt-injection pattern or a question under 3 characters | Fixed scope message |

## Design choices

- **BM25F + static Model2Vec embeddings + exact cosine.** The corpus is a few thousand chunks
  (~3.4k chunks / ~4.6k embedding pieces). Exact dot products over that are sub-millisecond, so an ANN
  index or vector database would add moving parts for no gain. Embeddings come from
  [go-potion](https://github.com/trengrj/go-potion) (POTION static models): computed in-process, no
  embedding API, no GPU, ~30 MB of weights memory-mapped. Keyword search keeps exact identifiers, config
  keys and error strings strong, and synonyms (`data/synonyms.yaml`) bridge vocabulary such as
  *compaction ↔ self-optimizing*.
- **One LLM call by default.** Retrieval quality is handled in code, so the default path is a single
  completion per uncached question. Query rewrite (`REWRITE`) and rerank (`RERANK`) are opt-in extra
  calls, and both fail safe: on a timeout or unparsable output, the request continues without them.
- **Index baked into the image.** The bundle is `go:embed`-ed into the binary: no database, no disk, no
  network at boot, and a cold start of about a second. A content refresh is a reseed plus a redeploy.
- **"Don't know" is decided by code, not by the model.** A relevance floor short-circuits weak
  retrieval before any LLM call. After the call, verification:
  - removes out-of-range citations and renumbers the rest;
  - rejects answers with zero valid citations;
  - strips links that are neither in the passages nor on olake.io / `github.com/datazip-inc`;
  - flags backticked identifiers that don't exist in the docs or code;
  - appends deprecation notes from `data/deprecated.yaml`.
- **Swap points.** Each is one adapter plus one `case` in `internal/app`:

  | Swap | Interface | Adapters |
  |---|---|---|
  | LLM provider | `domain.LLM` | `client/llm_openai.go` (any OpenAI-compatible API: Groq, OpenRouter, vLLM, …), `client/llm_anthropic.go` |
  | Embedder | `domain.Embedder` | `embed/model2vec.go` (local), `client/embedding_api.go` (OpenAI-compatible) |
  | Vector backend | `domain.Searcher` | `search/vector.go` (`VECTOR_BACKEND=memory`) |
  | Cache | `domain.Cache` | `middleware.NewLRUCache` (in-memory LRU + TTL) |

All tunable numbers live in `internal/tuning/tuning.go`, all content lists in `data/*.yaml`, and all
settings and secrets come from the environment.

## Configuration

Settings come only from the environment (a `.env` file in the working directory is loaded if present
and never overrides real variables). See `.env.example`.

| Var | Default | Allowed | Used by | Notes |
|---|---|---|---|---|
| `LLM_API_KEY` | – | secret | server, eval | empty → permanent `search_only` |
| `LLM_PROVIDER` | `openai_compatible` | `openai_compatible`, `anthropic` | server | |
| `LLM_BASE_URL` | `""` | URL | server | required for `openai_compatible` (e.g. `https://api.groq.com/openai/v1`); `anthropic` defaults to `https://api.anthropic.com` |
| `LLM_MODEL` | `""` | string | server | required for the LLM to be enabled |
| `LLM_FALLBACK_MODEL` | `""` | string | server | tried once if the answer call fails |
| `GITHUB_TOKEN` | – | secret | seeder | optional; unauthenticated runs cap issue comments at 50 |
| `EMBEDDER` | `model2vec` | `model2vec`, `api`, `none` | seeder | the server follows the bundle manifest |
| `EMBED_MODEL` | `RETRIEVAL32M` | go-potion model or API model name | seeder | the server follows the bundle manifest |
| `EMBED_BASE_URL` / `EMBED_API_KEY` | `""` | URL / secret | seeder, server | `EMBED_BASE_URL` only for `EMBEDDER=api`; `EMBED_API_KEY` also enables Cohere |
| `EMBED_API` | `auto` | `auto`, `off` | seeder, server | `auto`: build/use Cohere vectors when `EMBED_API_KEY` (and, for the seeder, `EMBED_API_MODEL`) is set; `off`: local vectors only |
| `EMBED_API_MODEL` | `""` | Cohere model, e.g. `embed-v4.0` | seeder | empty = no Cohere vectors; needs `EMBEDDER=model2vec` (local vectors are the fallback) |
| `EMBED_API_DIM` | `0` | Cohere `output_dimension` | seeder | 0 = model default; recorded in the manifest so queries match |
| `EMBED_API_BASE_URL` | `https://api.cohere.com/v2` | URL | seeder, server | |
| `VECTOR_BACKEND` | `memory` | `memory` | server | |
| `REWRITE` | `off` | `off`, `when_needed`, `always` | server | extra LLM call |
| `RERANK` | `off` | `off`, `on` | server | extra LLM call |
| `DAILY_TOKEN_BUDGET` | `5000000` | int ≥ 0 | server | `0` = unlimited; resets at 00:00 UTC |
| `ALLOWED_ORIGINS` | `https://olake.io,http://localhost:*,http://127.0.0.1:*` | comma list, `:*` port wildcard | server | CORS |
| `SEED_ISSUES` | `on` | `on`, `off` | seeder | |
| `PORT` | `8080` | int | server | Render sets it |
| `DEBUG` | `off` | `on`, `off` | server, eval | `on` prints every outgoing LLM request (method, URL, indented JSON body) to the terminal; headers and API keys are never printed. For local runs only. |

Invalid values stop the process with an error naming the variable.

Data files (embedded at build time):

| File | Purpose |
|---|---|
| `data/sources.yaml` | docs sitemap and filters, repositories (ref, globs, Helm values, error extraction), external pages, issue repos, community links |
| `data/synonyms.yaml` | equivalent phrases used for query expansion |
| `data/products.yaml` | URL → product rules and query terms for OLake vs OLake Fusion |
| `data/deprecated.yaml` | deprecated terms, notes and pages (ranking penalty + answer notes) |

## Running

Requires Go 1.25+ and `git` (seeder only).

```bash
make seed                          # crawl + index → assets/index (≈1 min)
make run                           # API on :8080 (search_only without an LLM key)
make demo API=http://localhost:8080   # widget demo on http://localhost:8081
make eval                          # retrieval eval → eval/report.md (no LLM needed)
make eval-full                     # end-to-end eval with the configured LLM
make test                          # offline unit tests
```

Rebuild or restart the server after `make seed`: the bundle is compiled into the binary.

## Deploy

**Render.** `render.yaml` describes a Docker web service on the free plan with `/healthz` as its health
check. Create a Blueprint from the repository, then set the secrets the file leaves unsynced:
`LLM_BASE_URL`, `LLM_MODEL`, `LLM_API_KEY` and optionally `LLM_FALLBACK_MODEL`. The Dockerfile builds a
static binary, pre-downloads the embedding model named in the bundle manifest (`cmd/fetchmodel`) and
ships both on `distroless/static`, so the container needs no network at startup.

```bash
make docker && docker run -p 8080:8080 -e LLM_API_KEY=… -e LLM_BASE_URL=… -e LLM_MODEL=… olake-ask
```

**Docusaurus widget.** Add the script to `docusaurus.config.js`:

```js
scripts: [{src: 'https://<host>/widget.js', defer: true}],
```

The widget calls the API on the script's own origin (override with `data-api-url`, and the title with
`data-title`). It follows Docusaurus' `data-theme` for light and dark mode, renders into a shadow DOM so
site CSS can't leak in, and escapes all server text before rendering. Add the docs origin to
`ALLOWED_ORIGINS`.

## Refreshing content

Re-run the seeder and redeploy. The seeder is CI-ready: it needs Go, `git` and network access, and
optionally `GITHUB_TOKEN`. On any fatal problem (docs crawl or extraction failure, self-check failure) it
exits 1 and leaves the previous bundle untouched. Optional sources (repos, errors, external pages, issues,
embeddings) only warn. Pin a repository by setting its `ref` in `data/sources.yaml` to a commit SHA.

## Eval results

EVAL_TABLE_PLACEHOLDER

## Limitations

- Single-turn: every question is independent (no chat history).
- Cache, daily token budget and rate limits are in memory per instance and reset on restart.
- Answers are only as fresh as the last seed; the docs are not searched at runtime.
- Questions about features that don't exist but use OLake vocabulary pass the relevance floor; they rely
  on the LLM answering "not answerable" (measured by `make eval-full`).
