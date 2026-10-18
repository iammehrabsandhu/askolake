#!/usr/bin/env bash
# Pings the service every 10 minutes so Render's free tier does not spin it
# down after 15 minutes of inactivity.
#
# Usage: ./scripts/keepalive.sh https://olake-ask.onrender.com
#   or:  URL=https://olake-ask.onrender.com INTERVAL=600 ./scripts/keepalive.sh
set -u

URL="${1:-${URL:-}}"
INTERVAL="${INTERVAL:-600}"

if [ -z "$URL" ]; then
  echo "usage: $0 <service-base-url>" >&2
  exit 1
fi
URL="${URL%/}/ping"

echo "keep-alive: pinging $URL every ${INTERVAL}s (Ctrl+C to stop)"
while true; do
  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 60 "$URL")
  echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) $URL -> ${code}"
  sleep "$INTERVAL"
done
