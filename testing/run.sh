#!/usr/bin/env bash
# Capture the exact LLM request for every e2e.md case and compare it with the
# expected output. No LLM provider is called. All flags go to cmd/e2e:
#
#   testing/run.sh                                  # defaults, in-process capture
#   testing/run.sh -name cap4 -set PerPageCap=4     # one knob
#   testing/run.sh -knobs testing/knobs/experiments/old-defaults.yaml
#   testing/run.sh -data testing/data/my-exp        # alternative data/*.yaml
#   testing/run.sh -reseed -set ChunkMaxWords=250   # seed-time knobs (network, ~30-60 s)
#   testing/run.sh -index testing/runs/<run>/index  # reuse a reseeded bundle
#   testing/run.sh -server                          # real server, DEBUG=on, dead LLM URL
#   testing/run.sh -dump-knobs                      # list every knob and default
#
# Output: testing/runs/<timestamp>-<name>/summary.md (read this first),
# cases.md, requests/NN.txt, golden.md, knobs.yaml.
set -euo pipefail
cd "$(dirname "$0")/.."
exec go run ./cmd/e2e "$@"
