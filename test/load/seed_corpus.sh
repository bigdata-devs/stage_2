#!/usr/bin/env bash
# Seeds the datamart with real Project Gutenberg books for the search benchmark (audit RF 5). It
# walks Gutenberg book IDs upwards from FROM_ID, ingests each one through the Ingestion Service and
# indexes it through the Indexing Service, until TARGET books are indexed. IDs Gutenberg cannot
# serve (no plain-text edition, withdrawn) are skipped; temporary failures are retried. Both calls
# are idempotent, so a rerun only downloads what the datalake is missing.
#
# Usage, from the repository root with the stack up (docker compose up -d --build):
#   test/load/seed_corpus.sh            index at least 300 books
#   test/load/seed_corpus.sh 500        index at least 500 books
#
# Environment (all optional):
#   INGESTION_URL  Ingestion Service base URL                    default http://localhost:7001
#   INDEXING_URL   Indexing Service base URL                     default http://localhost:7002
#   FROM_ID        first Gutenberg ID tried                      default 1
#   MAX_ID         last Gutenberg ID tried                       default FROM_ID + 2 x TARGET
#   DELAY          seconds to pause after each real download     default 0.5 (be polite to gutenberg.org)
#   RETRIES        extra attempts after a temporary failure      default 2
#
# The Ingestion Service downloads from gutenberg.org, so the machine running the stack needs
# internet access. Expect roughly 10 to 20 minutes for 300 books on a first run.
set -euo pipefail

TARGET="${1:-300}"
INGESTION_URL="${INGESTION_URL:-http://localhost:7001}"
INGESTION_URL="${INGESTION_URL%/}"
INDEXING_URL="${INDEXING_URL:-http://localhost:7002}"
INDEXING_URL="${INDEXING_URL%/}"
FROM_ID="${FROM_ID:-1}"
MAX_ID="${MAX_ID:-$((FROM_ID + 2 * TARGET))}"
DELAY="${DELAY:-0.5}"
RETRIES="${RETRIES:-2}"

case "$TARGET" in
  '' | *[!0-9]*) echo "TARGET must be a positive integer, got '$TARGET'" >&2; exit 2 ;;
esac

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

# post URL: sends a POST and prints the HTTP status; the body lands in $workdir/body.
post() {
  curl -sS -X POST -o "$workdir/body" -w '%{http_code}' --max-time 300 "$1" 2>/dev/null || echo 000
}

body_field() {
  { grep -o "\"$1\":\"[^\"]*\"" "$workdir/body" 2>/dev/null || true; } | head -1 | cut -d'"' -f4
}

wait_ready() {
  for _ in $(seq 1 60); do
    if curl -fsS -o /dev/null --max-time 5 "$1" 2>/dev/null; then
      return 0
    fi
    sleep 1
  done
  echo "FAIL: $2 does not answer $1; start the stack with: docker compose up -d --build" >&2
  exit 1
}

# ingest ID: prints the ingestion status (downloaded, available, unavailable or failed), retrying
# temporary failures. The status comes from the body when the service sends one; otherwise HTTP 404
# (book_unavailable: Gutenberg has no such book) is permanent, and anything else, including a
# transport error, counts as a temporary failure.
ingest() {
  local book="$1" attempt status code
  for attempt in $(seq 0 "$RETRIES"); do
    code="$(post "$INGESTION_URL/ingest/$book")"
    status="$(body_field status)"
    if [ -z "$status" ]; then
      case "$code" in
        404) status="unavailable" ;;
        *) status="failed" ;;
      esac
    fi
    if [ "$status" != failed ]; then
      echo "$status"
      return 0
    fi
    if [ "$attempt" -lt "$RETRIES" ]; then
      echo "     book $book: temporary failure (HTTP $code), retrying in $((5 * (attempt + 1)))s" >&2
      sleep $((5 * (attempt + 1)))
    fi
  done
  echo failed
}

wait_ready "$INGESTION_URL/ingest/list" "the Ingestion Service"
wait_ready "$INDEXING_URL/index/status" "the Indexing Service"
echo "== seeding at least $TARGET books, trying Gutenberg IDs $FROM_ID..$MAX_ID =="

indexed=0
skipped=0
started=$(date +%s)
book="$FROM_ID"
while [ "$indexed" -lt "$TARGET" ] && [ "$book" -le "$MAX_ID" ]; do
  status="$(ingest "$book")"
  case "$status" in
    downloaded | available)
      code="$(post "$INDEXING_URL/index/update/$book")"
      if [[ "$code" == 2?? ]]; then
        indexed=$((indexed + 1))
        printf '[%3d/%d] book %-6s %-10s indexed (%ss elapsed)\n' "$indexed" "$TARGET" "$book" "$status" "$(($(date +%s) - started))"
      else
        skipped=$((skipped + 1))
        echo "     book $book: ingested but indexing answered HTTP $code: $(cat "$workdir/body" 2>/dev/null)" >&2
      fi
      [ "$status" = downloaded ] && sleep "$DELAY"
      ;;
    *)
      skipped=$((skipped + 1))
      echo "     book $book: skipped ($status)"
      ;;
  esac
  book=$((book + 1))
done

status_body="$(curl -fsS --max-time 10 "$INDEXING_URL/index/status" || echo '{}')"
published="$(grep -o '"books_indexed":[0-9]*' <<<"$status_body" | cut -d: -f2 || true)"
echo "== indexed $indexed books in this run, skipped $skipped IDs, last ID tried $((book - 1)) =="
echo "== GET /index/status: $status_body =="
if [ "${published:-0}" -lt "$TARGET" ]; then
  echo "FAIL: the datamart publishes ${published:-0} books, fewer than $TARGET; raise MAX_ID and rerun" >&2
  exit 1
fi
