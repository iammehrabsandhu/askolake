# Reviewer notes

Go + Gin RAG service over the OLake docs and repos. Live at https://askolake.onrender.com (Render, Docker, free plan, so the first request after 15 min idle is slow).

## Endpoints

| Route | What it does |
|---|---|
| `POST /ask` | `{"question": "..."}` → `{"answer", "sources"}`. Answer cites `[n]` into `sources`. Off-topic questions get "I don't know" with no LLM call. |
| `GET /version` | `{"commit", "deployment"}`, read from env on every request (`RENDER_GIT_COMMIT` / `RENDER_SERVICE_ID`, or the Vercel equivalents). |
| `GET /models` | Model IDs OpenRouter lists for our key (`/api/v1/models/user`), fetched live. |
| `GET /demo` | Demo page with the chat widget, talking to this server. |
| `GET /widget.js` | The embeddable widget. Drop it into any page with a `<script>` tag. |
| `GET /healthz` | Index version, chunk count, whether vectors and the LLM are on. |
| `GET /ping` | Returns `pong`. For keep-alive pingers so the free instance doesn't sleep. |
| `GET /` | Plain-text banner. |

CORS is open (`*`). Key comes from `OPENROUTER_API_KEY`.

## Where the index comes from (read this)

The server never crawls anything. The index is built offline by the **seeder**, `cmd/seeder` (`make seed`), and committed to `assets/index/`. The Docker build compiles it into the binary with `go:embed`.

Treat the seeder as a batch/cron job: run it on a schedule (or by hand), commit the new `assets/index/`, and the next deploy picks it up. There's no scheduler in this repo; that's on whoever runs it. It crawls olake.io/docs, clones the four OLake repos, pulls closed GitHub issues (set `GITHUB_TOKEN`), chunks, embeds and writes the bundle. About 2 min.

## Features

- Hybrid retrieval: BM25F keyword search + local RETRIEVAL32M embeddings (go-potion, no embedding API) + a matcher that maps pasted Go error messages to the code that raises them.
- RRF fusion with source weights, product boost (OLake vs Fusion), deprecated and low-value penalties, a per-page cap and duplicate removal at query time.
- Relevance floor: off-topic questions stop before the LLM. Product-only questions ("What is OLake?") get the overview sections pinned first.
- One LLM call per question by default. OpenRouter fallbacks in priority order: glm-4.7-flash → gemini-2.5-flash-lite → deepseek-v4.1-flash.
- Answer verification: citations must point at real passages, unknown URLs are stripped, unknown config keys and identifiers are flagged, deprecated features get a note.
- Input guards (prompt injection, secret redaction), an answer cache with request de-duplication, per-IP and global rate limits, a daily token budget, a 25 s request cap.
- Optional extra LLM calls (`REWRITE`, `RERANK`) and optional Cohere embeddings with fallback to local vectors. All off by default.
- Widget: vanilla JS, shadow DOM, escapes all server text, follows Docusaurus light and dark mode.
- Test harness: `testing/run.sh` captures the exact LLM request for each `e2e.md` case and checks sources and facts, without calling an LLM. ~5 s per run, knobs turnable from the command line. See `testing/README.md`.

More detail: `README.md` (setup, env vars), `context.md` (architecture, tuning notes).
