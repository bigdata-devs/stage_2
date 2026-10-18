#!/usr/bin/env bash
# End-to-end evidence of the Control Module: it builds the real Ingestion and
# Indexing Services plus the control binary, runs the three processes over a
# temporary datalake on high ports, and asserts the pipeline outcome, the
# restart semantics, the permanent-failure recording and the discovery mode.
set -euo pipefail

INGESTION_PORT="${INGESTION_PORT:-17001}"
INDEXING_PORT="${INDEXING_PORT:-17002}"
WORK_DIR="$(mktemp -d)"
BIN_DIR="$WORK_DIR/bin"
LOG_DIR="$WORK_DIR/logs"
DATALAKE_DIR="$WORK_DIR/datalake"
STATE_DIR="$WORK_DIR/control"
mkdir -p "$BIN_DIR" "$LOG_DIR"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

pass() {
  echo "PASS: $*"
}

cleanup() {
  kill "${INGESTION_PID:-}" "${INDEXING_PID:-}" 2>/dev/null || true
}
trap cleanup EXIT

wait_ready() {
  local url="$1"
  local name="$2"
  for _ in $(seq 1 60); do
    if curl -fsS "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.5
  done
  fail "$name never answered $url"
}

run_control() {
  "$BIN_DIR/control" \
    -ingestion-url "http://localhost:$INGESTION_PORT" \
    -indexing-url "http://localhost:$INDEXING_PORT" \
    -state-dir "$STATE_DIR" \
    -workers 2 \
    -ingest-timeout 120s \
    "$@"
}

summary_field() {
  local log_file="$1"
  local field="$2"
  grep -o "${field}=[0-9]*" "$log_file" | tail -1 | cut -d= -f2
}

echo "== building the three binaries =="
go build -o "$BIN_DIR/ingestion" ./cmd/ingestion
go build -o "$BIN_DIR/indexing" ./cmd/indexing
go build -o "$BIN_DIR/control" ./cmd/control
pass "ingestion, indexing and control binaries built"

echo "== starting both services on high ports =="
"$BIN_DIR/ingestion" -addr ":$INGESTION_PORT" -datalake "$DATALAKE_DIR" >"$LOG_DIR/ingestion.log" 2>&1 &
INGESTION_PID=$!
"$BIN_DIR/indexing" -addr ":$INDEXING_PORT" -datalake "$DATALAKE_DIR" >"$LOG_DIR/indexing.log" 2>&1 &
INDEXING_PID=$!
wait_ready "http://localhost:$INGESTION_PORT/ingest/list" "ingestion"
wait_ready "http://localhost:$INDEXING_PORT/index/status" "indexing"
pass "both services answer their documented status routes"

echo "== orchestration of two real books =="
run_control -ids 1342,84 >"$LOG_DIR/control_run1.log" 2>&1
for book in 1342 84; do
  grep -qx "$book" "$STATE_DIR/downloaded_books.txt" || fail "book $book missing from downloaded_books.txt"
  grep -qx "$book" "$STATE_DIR/indexed_books.txt" || fail "book $book missing from indexed_books.txt"
done
pass "books 1342 and 84 are downloaded and indexed in the control state"

[ "$(summary_field "$LOG_DIR/control_run1.log" indexed)" = "2" ] || fail "first run did not report 2 indexed books"
datalake_count="$(curl -fsS "http://localhost:$INGESTION_PORT/ingest/list" | grep -o '"count":[0-9]*' | cut -d: -f2)"
indexed_count="$(curl -fsS "http://localhost:$INDEXING_PORT/index/status" | grep -o '"books_indexed":[0-9]*' | cut -d: -f2)"
[ "$datalake_count" -ge 2 ] || fail "datalake reports $datalake_count books, expected at least 2"
[ "$indexed_count" -ge 2 ] || fail "index reports $indexed_count books, expected at least 2"
pass "services agree: datalake has $datalake_count books, index has $indexed_count"

echo "== restart semantics: a replayed run skips finished books =="
run_control -ids 1342,84 >"$LOG_DIR/control_run2.log" 2>&1
[ "$(summary_field "$LOG_DIR/control_run2.log" skipped)" = "2" ] || fail "replayed run did not skip both books"
[ "$(summary_field "$LOG_DIR/control_run2.log" indexed)" = "0" ] || fail "replayed run indexed a book again"
[ "$(wc -l < "$STATE_DIR/indexed_books.txt")" -eq 2 ] || fail "replayed run duplicated index records"
pass "replayed run skips both books and keeps the state files intact"

echo "== permanent failure: an unknown book is recorded once =="
run_control -ids 999999999 >"$LOG_DIR/control_run3.log" 2>&1
[ "$(summary_field "$LOG_DIR/control_run3.log" failed)" = "1" ] || fail "unknown book was not reported as failed"
grep -q "^999999999" "$STATE_DIR/failed_books.txt" || fail "unknown book missing from failed_books.txt"
[ "$(grep -c "^999999999" "$STATE_DIR/failed_books.txt")" -eq 1 ] || fail "unknown book recorded more than once"
pass "unknown book recorded once in failed_books.txt with its reason"

echo "== discovery mode: candidates come from GET /ingest/list =="
run_control -discover >"$LOG_DIR/control_run4.log" 2>&1
grep -q "discovered complete books" "$LOG_DIR/control_run4.log" || fail "discover run listed no books"
[ "$(summary_field "$LOG_DIR/control_run4.log" skipped)" = "2" ] || fail "discover run did not skip the two already indexed books"
[ "$(wc -l < "$STATE_DIR/indexed_books.txt")" -eq 2 ] || fail "discover run changed the indexed set"
pass "discover run processes the datalake listing and skips finished books"

echo "ALL E2E CHECKS PASSED"
echo "evidence under $WORK_DIR"
