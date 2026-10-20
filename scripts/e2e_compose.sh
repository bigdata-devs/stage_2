#!/usr/bin/env bash
# End-to-end evidence of the Docker Compose deployment: it builds the four images,
# starts MongoDB plus the three services, orchestrates real books from inside the
# compose network, and proves the two properties of the stack — a killed service
# container does not break the pipeline, and named volumes survive recreation.
set -euo pipefail

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

pass() {
  echo "PASS: $*"
}

wait_ready() {
  local url="$1"
  local name="$2"
  for _ in $(seq 1 90); do
    if curl -fsS "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  docker compose ps >&2
  fail "$name never answered $url"
}

run_control() {
  docker compose --profile tools run --rm control "$@"
}

summary_field() {
  local text="$1"
  local field="$2"
  grep -o "${field}=[0-9]*" <<<"$text" | tail -1 | cut -d= -f2
}

echo "== building images and starting the stack =="
docker compose --profile tools down -v --remove-orphans >/dev/null 2>&1 || true
docker compose up -d --build
wait_ready "http://localhost:7001/ingest/list" "ingestion"
wait_ready "http://localhost:7002/index/status" "indexing"
wait_ready "http://localhost:7003/search/filters" "search"
docker compose ps
pass "mongo, ingestion, indexing and search are up and healthy"

echo "== orchestration of two real books from inside the compose network =="
run1="$(run_control -ids 1342,84 2>&1)" || { echo "$run1"; fail "control run failed"; }
[ "$(summary_field "$run1" indexed)" = "2" ] || { echo "$run1"; fail "expected 2 indexed books"; }
datalake_count="$(curl -fsS http://localhost:7001/ingest/list | grep -o '"count":[0-9]*' | cut -d: -f2)"
indexed_count="$(curl -fsS http://localhost:7002/index/status | grep -o '"books_indexed":[0-9]*' | cut -d: -f2)"
[ "$datalake_count" -ge 2 ] || fail "datalake reports $datalake_count books, expected at least 2"
[ "$indexed_count" -ge 2 ] || fail "index reports $indexed_count books, expected at least 2"
pass "books 1342 and 84 ingested, indexed and published (datalake=$datalake_count, datamart=$indexed_count)"

echo "== replayed run skips finished books =="
run2="$(run_control -ids 1342,84 2>&1)" || { echo "$run2"; fail "replayed run failed"; }
[ "$(summary_field "$run2" skipped)" = "2" ] || { echo "$run2"; fail "expected 2 skipped books"; }
pass "replayed run skips both books (idempotent, no duplicates)"

echo "== front-facing search answers from the datamart =="
curl -fsS "http://localhost:7003/search?q=adventure" >/dev/null || fail "GET /search does not answer"
filters="$(curl -fsS http://localhost:7003/search/filters)" || fail "GET /search/filters does not answer"
curl -fsS "http://localhost:7003/books/1342" >/dev/null || fail "GET /books/1342 does not answer"
curl -fsS -o /dev/null http://localhost:7003/swagger/index.html || fail "swagger ui does not answer"
pass "search API answers /search, /search/filters, /books/1342 and swagger"

echo "== failure recovery: the ingestion container dies =="
docker compose stop ingestion >/dev/null
down_run="$(docker compose --profile tools run --rm --no-deps control -ids 5 2>&1 || true)"
if grep -q "ingestion service is not ready" <<<"$down_run"; then
  pass "control refuses to claim books while ingestion is down (fail fast)"
else
  echo "$down_run"
  fail "control did not detect the stopped ingestion service"
fi
docker compose start ingestion >/dev/null
wait_ready "http://localhost:7001/ingest/list" "ingestion (restarted)"
run3="$(run_control -ids 5 2>&1)" || { echo "$run3"; fail "run after recovery failed"; }
[ "$(summary_field "$run3" indexed)" = "1" ] || { echo "$run3"; fail "book 5 not indexed after recovery"; }
pass "ingestion relaunched, book 5 indexed: the pipeline survived the outage"

echo "== named volumes survive container recreation =="
docker compose down >/dev/null
docker compose up -d >/dev/null
wait_ready "http://localhost:7001/ingest/list" "ingestion (recreated)"
wait_ready "http://localhost:7002/index/status" "indexing (recreated)"
wait_ready "http://localhost:7003/search/filters" "search (recreated)"
datalake_count="$(curl -fsS http://localhost:7001/ingest/list | grep -o '"count":[0-9]*' | cut -d: -f2)"
indexed_count="$(curl -fsS http://localhost:7002/index/status | grep -o '"books_indexed":[0-9]*' | cut -d: -f2)"
[ "$datalake_count" -ge 3 ] || fail "datalake lost books after recreation (count=$datalake_count)"
[ "$indexed_count" -ge 3 ] || fail "datamart lost books after recreation (count=$indexed_count)"
run4="$(run_control -discover 2>&1)" || { echo "$run4"; fail "discover after recreation failed"; }
[ "$(summary_field "$run4" skipped)" = "3" ] || { echo "$run4"; fail "expected 3 skipped books after recreation"; }
pass "containers recreated, datalake and datamart intact, control state resumed"

echo "== teardown =="
docker compose --profile tools down -v >/dev/null
pass "stack removed with its volumes"

echo "ALL COMPOSE E2E CHECKS PASSED"
