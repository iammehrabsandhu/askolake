# Retrieval test harness

`testing/run.sh` answers every case in `eval/e2e.jsonl` (the machine twin of
`e2e.md`) through the real answer chain, records the exact request that would
go to the LLM (**askolake_llm_log**), and compares it with the
**expected_output**. No LLM provider is ever called.

A case passes when:

| Check | Meaning |
|---|---|
| Outcome | `llm` (a request was sent), `refused` (input guard) or `dont_know` (relevance floor), as the case allows |
| Source | a passage URL contains one of `expect_url_contains` (the e2e.md source page) |
| Facts | every `must_contain` string is in the passages (titles + text, case-insensitive), not counting the question |
| Forbidden | no `must_not_contain` regex matches the passages (e.g. invented SLA figures) |

Behaviour-only must-mention terms from e2e.md ("not supported", "couldn't
find") describe the *answer*, so they can only be checked with a real LLM.
The harness checks the context that answer would be built from.

## Run

```bash
testing/run.sh                       # defaults, ~5 s incl. golden set
testing/run.sh -name exp -set BM25B=0.5 -set PerPageCap=4
testing/run.sh -knobs testing/knobs/experiments/near-dup-0.8.yaml
testing/run.sh -server               # final check: real server binary, DEBUG=on, dead LLM URL (~2 min)
```

Read `testing/runs/<timestamp>-<name>/summary.md` first: one line per case plus
the golden-set totals with the same settings. `cases.md` shows each case's
e2e.md row next to the passages that were sent, marking the expected source and
which passage holds which fact. `requests/NN.txt` is the exact request.

## What you can turn

| Kind | How | Takes effect |
|---|---|---|
| Ranking knobs (BM25, title weight, RRF, page cap, near-dup, penalties, floors, source weights, top-K, context size) | `-set NAME=VALUE` or `-knobs file.yaml` | instantly |
| Synonyms, `low_value`, products, deprecated pages | `-data dir` (a copy of `data/` with edits) | instantly (flags are re-applied from the data in use) |
| Chunking and piece knobs, sources, dedupe | `-reseed` (+ seed knobs / `-data`) | ~30-60 s, needs network; written to the run's `index/`, never `assets/index/` |
| A bundle from an earlier run | `-index testing/runs/<run>/index` | instantly |

`testing/knobs/default.yaml` lists every knob with its default
(regenerate: `testing/run.sh -dump-knobs > testing/knobs/default.yaml`).
`testing/knobs/experiments/` holds ready-made experiments; files only need the
knobs they change.

`-server` uses the compiled-in tuning, data and bundle (it is the production
binary), so it refuses `-knobs/-set/-data/-index/-reseed`. To ship an
experiment, copy the winning values into `internal/tuning/tuning.go` /
`data/*.yaml`, `make seed` if needed, and confirm with `testing/run.sh -server`.

Production never reads knob files: the knob registry (`tuning.Set`) is only
called by `cmd/e2e`.
