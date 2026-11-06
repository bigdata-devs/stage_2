#!/usr/bin/env bash
# API validation of the Boogle Engine Search API (audit RF 10). It runs every documented edge
# case of GET /search, GET /books/{book_id} and GET /search/filters against a running stack and
# checks status codes, error codes and response invariants. It is a regression test (exit status
# 1 when any check fails) and the generator of docs/evidence/api_validation_results.md.
#
# Usage, from the repository root with the stack up (docker compose up -d --build):
#   scripts/api_validation.sh                  seed the fixture books, then validate
#   scripts/api_validation.sh --no-seed        validate only (needs just the Search Service)
#   scripts/api_validation.sh --record docs/evidence/api_validation_results.md
#
# Environment (all optional):
#   SEARCH_URL        Search Service base URL                   default http://localhost:7003
#   INGESTION_URL     Ingestion Service base URL                default http://localhost:7001
#   INDEXING_URL      Indexing Service base URL                 default http://localhost:7002
#   CORS_TEST_ORIGIN  Origin sent by the CORS preflight check   default http://localhost:5173
#   STACK_DESCRIPTION free text describing the deployment, written into the --record header
#
# Seeding ingests and indexes four Project Gutenberg books through the stack's own APIs: 82
# (Ivanhoe), 84 (Frankenstein), 1341 (The Altruist in Politics) and 1342 (Pride and Prejudice).
# Both steps are idempotent, so the script also runs against a stack that already holds them or
# a larger corpus: checks assert what those four books guarantee (an ID that must or must not
# appear, an error code, a filter every result must satisfy), never corpus-dependent totals.
#
# Needs bash, curl and grep (Git Bash on Windows works). python3 or jq, when present, only
# pretty-print the response bodies written by --record.
set -euo pipefail

SEARCH_URL="${SEARCH_URL:-http://localhost:7003}"
SEARCH_URL="${SEARCH_URL%/}"
INGESTION_URL="${INGESTION_URL:-http://localhost:7001}"
INGESTION_URL="${INGESTION_URL%/}"
INDEXING_URL="${INDEXING_URL:-http://localhost:7002}"
INDEXING_URL="${INDEXING_URL%/}"
CORS_TEST_ORIGIN="${CORS_TEST_ORIGIN:-http://localhost:5173}"
FIXTURE_BOOKS="82 84 1341 1342"

seed=1
record=""
while [ $# -gt 0 ]; do
  case "$1" in
    --no-seed) seed=0 ;;
    --record)
      record="${2:-}"
      [ -n "$record" ] || { echo "--record needs a file name" >&2; exit 2; }
      shift
      ;;
    -h | --help)
      sed -n '2,/^set -euo pipefail/p' "$0" | sed '$d; s/^# \{0,1\}//'
      exit 0
      ;;
    *) echo "unknown argument: $1 (try --help)" >&2; exit 2 ;;
  esac
  shift
done

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT
: >"$workdir/sections.md"
: >"$workdir/table.md"
: >"$workdir/seed.md"

STATUS=""
BODY=""
failures=()
case_count=0
failed_count=0

# http_call METHOD URL [curl options...] stores the status in STATUS, the body in BODY and the
# response headers in $workdir/headers. A transport failure leaves STATUS=000.
http_call() {
  local method="$1" url="$2"
  shift 2
  STATUS="$(curl -sS -X "$method" -o "$workdir/body" -D "$workdir/headers" -w '%{http_code}' \
    --max-time "${CURL_TIMEOUT:-120}" "$@" "$url" 2>"$workdir/curl_error")" || STATUS="000"
  BODY="$(cat "$workdir/body" 2>/dev/null || true)"
}

header_value() {
  grep -i "^$1:" "$workdir/headers" 2>/dev/null | head -1 | cut -d: -f2- | tr -d '\r' | sed 's/^ *//' || true
}

json_number() {
  { grep -o "\"$1\":[0-9]*" <<<"$BODY" || true; } | head -1 | cut -d: -f2
}

wait_ready() {
  local url="$1" name="$2"
  for _ in $(seq 1 60); do
    if curl -fsS -o /dev/null --max-time 5 "$url" 2>/dev/null; then
      return 0
    fi
    sleep 1
  done
  echo "FAIL: $name does not answer $url; start the stack with: docker compose up -d --build" >&2
  exit 1
}

# ---------------------------------------------------------------------------------------------
# Assertions: each one records a reason in failures when it does not hold.
# ---------------------------------------------------------------------------------------------

expect_status() {
  [ "$STATUS" = "$1" ] || failures+=("HTTP $STATUS, expected $1")
}

expect_contains() {
  case "$BODY" in
    *"$1"*) ;;
    *) failures+=("body lacks $1") ;;
  esac
}

expect_absent() {
  case "$BODY" in
    *"$1"*) failures+=("body contains $1") ;;
  esac
}

expect_total_positive() {
  local total
  total="$(json_number total)"
  if [ -z "$total" ] || [ "$total" -le 0 ]; then
    failures+=("total is '${total}', expected more than 0")
  fi
}

# expect_every_string FIELD REGEX: every "FIELD":"value" in the body (the filters echo and each
# result) matches the case-insensitive extended regular expression.
expect_every_string() {
  local field="$1" pattern="$2" value
  while IFS= read -r value; do
    if ! grep -qiE "$pattern" <<<"$value"; then
      failures+=("$field \"$value\" does not match /$pattern/")
    fi
  done < <({ grep -o "\"$field\":\"[^\"]*\"" <<<"$BODY" || true; } | sed "s/^\"$field\":\"//; s/\"\$//")
}

# expect_every_number FIELD N: every "FIELD":number in the body equals N.
expect_every_number() {
  local field="$1" want="$2" value
  while IFS= read -r value; do
    [ "$value" = "$want" ] || failures+=("$field $value, expected $want")
  done < <({ grep -o "\"$field\":[0-9]*" <<<"$BODY" || true; } | cut -d: -f2)
}

expect_header_in() {
  local name="$1" value
  shift
  value="$(header_value "$name")"
  for allowed in "$@"; do
    [ "$value" = "$allowed" ] && return 0
  done
  failures+=("header $name is '$value', expected one of: $*")
}

# ---------------------------------------------------------------------------------------------
# Cases
# ---------------------------------------------------------------------------------------------

current_id=""
current_title=""
current_expect=""
current_request=""
current_headers=()

# begin_case ID TITLE EXPECTATION METHOD PATH [curl options...] sends the request.
begin_case() {
  current_id="$1"
  current_title="$2"
  current_expect="$3"
  local method="$4" path="$5" previous="" argument
  shift 5
  current_request="$method $path"
  current_headers=()
  for argument in "$@"; do
    [ "$previous" = "-H" ] && current_headers+=("$argument")
    previous="$argument"
  done
  failures=()
  http_call "$method" "$SEARCH_URL$path" "$@"
}

pretty_body() {
  if command -v python3 >/dev/null 2>&1 &&
    python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin), indent=2, ensure_ascii=False))' <<<"$BODY" 2>/dev/null; then
    :
  elif command -v jq >/dev/null 2>&1 && jq . <<<"$BODY" 2>/dev/null; then
    :
  else
    echo "$BODY"
  fi
}

end_case() {
  local result="PASS" reason
  case_count=$((case_count + 1))
  if [ "${#failures[@]}" -gt 0 ]; then
    result="FAIL"
    failed_count=$((failed_count + 1))
  fi
  printf '%-4s %-4s %-3s %-44s %s\n' "$result" "$current_id" "$STATUS" "$current_title" "$current_request"
  if [ "${#failures[@]}" -gt 0 ]; then
    for reason in "${failures[@]}"; do
      echo "            - $reason"
    done
  fi
  [ -n "$record" ] || return 0
  # shellcheck disable=SC2016 # backticks are Markdown code spans, not command substitution
  printf '| %s | %s | `%s` | %s | %s | %s |\n' "$current_id" "$current_title" "$current_request" \
    "$current_expect" "$STATUS" "$result" >>"$workdir/table.md"
  {
    printf '\n### %s · %s\n\n' "$current_id" "$current_title"
    printf '```http\n%s %s\n' "${current_request%% *}" "$SEARCH_URL${current_request#* }"
    if [ "${#current_headers[@]}" -gt 0 ]; then
      printf '%s\n' "${current_headers[@]}"
    fi
    printf '```\n\n'
    printf '**Expected:** %s  \n**Actual:** HTTP %s — **%s**\n' "$current_expect" "$STATUS" "$result"
    if [ "${#failures[@]}" -gt 0 ]; then
      for reason in "${failures[@]}"; do
        printf -- '- %s\n' "$reason"
      done
    fi
    if grep -qiE '^(access-control-|vary:)' "$workdir/headers" 2>/dev/null; then
      printf '\nResponse headers:\n\n```\n'
      grep -iE '^(access-control-|vary:)' "$workdir/headers" | tr -d '\r'
      printf '```\n'
    fi
    if [ -z "$BODY" ]; then
      printf '\nEmpty body.\n'
    else
      printf '\n```json\n'
      pretty_body
      printf '```\n'
    fi
  } >>"$workdir/sections.md"
}

# ---------------------------------------------------------------------------------------------
# Seeding through the stack's own APIs
# ---------------------------------------------------------------------------------------------

wait_ready "$SEARCH_URL/search/filters" "the Search Service"
if [ "$seed" = 1 ]; then
  wait_ready "$INGESTION_URL/ingest/list" "the Ingestion Service"
  wait_ready "$INDEXING_URL/index/status" "the Indexing Service"
  echo "== seeding fixture books $FIXTURE_BOOKS =="
  for book in $FIXTURE_BOOKS; do
    http_call POST "$INGESTION_URL/ingest/$book"
    ingest_status="$STATUS"
    ingest_body="$BODY"
    if [[ "$ingest_status" != 2?? ]]; then
      echo "FAIL: POST $INGESTION_URL/ingest/$book answered HTTP $ingest_status: $ingest_body" >&2
      echo "      the Ingestion Service downloads from gutenberg.org; rerun with --no-seed to skip seeding" >&2
      exit 1
    fi
    http_call POST "$INDEXING_URL/index/update/$book"
    if [[ "$STATUS" != 2?? ]]; then
      echo "FAIL: POST $INDEXING_URL/index/update/$book answered HTTP $STATUS: $BODY" >&2
      exit 1
    fi
    echo "ok   book $book ingested (HTTP $ingest_status) and indexed: $BODY"
    # shellcheck disable=SC2016 # backticks are Markdown code spans, not command substitution
    printf '| %s | `POST /ingest/%s` → %s | `POST /index/update/%s` → %s `%s` |\n' \
      "$book" "$book" "$ingest_status" "$book" "$STATUS" "$BODY" >>"$workdir/seed.md"
  done
fi

echo "== validating $SEARCH_URL =="

# --- GET /search: keyword, filters, empty and past-the-end pages ------------------------------

begin_case S1 "Keyword search" "200; book 84 (Frankenstein) among the results" \
  GET "/search?q=frankenstein"
expect_status 200
expect_contains '"query":"frankenstein"'
expect_contains '"book_id":84,'
end_case

begin_case S2 "Author filter" "200; book 1342 present; every author contains \"austen\"" \
  GET "/search?q=adventure&author=Austen"
expect_status 200
expect_contains '"filters":{"author":"Austen"}'
expect_contains '"book_id":1342,'
expect_every_string author 'austen'
end_case

begin_case S3 "Language filter (code is case-insensitive)" "200; filter echoed as \"en\"; every result lists en" \
  GET "/search?q=adventure&language=EN"
expect_status 200
expect_contains '"filters":{"language":"en"}'
expect_total_positive
expect_every_string language '(^|,) *en *(,|$)'
end_case

begin_case S4 "Year filter" "200; filter echoed; every result has year 1818" \
  GET "/search?q=adventure&year=1818"
expect_status 200
expect_contains '"filters":{"year":1818}'
expect_every_number year 1818
end_case
year_total="$(json_number total)"

begin_case S5 "Combined filters (AND)" "200; only Austen's English books: 1342 present, 82 and 84 absent" \
  GET "/search?q=adventure&author=austen&language=en&limit=5"
expect_status 200
expect_contains '"filters":{"author":"austen","language":"en"}'
expect_contains '"limit":5'
expect_contains '"book_id":1342,'
expect_absent '"book_id":82,'
expect_absent '"book_id":84,'
expect_every_string author 'austen'
end_case

begin_case S6 "Keyword without matches" "200; total 0; results []" \
  GET "/search?q=quidditch"
expect_status 200
expect_contains '"total":0,'
expect_contains '"results":[]'
end_case

begin_case S7 "Offset past the last match" "200; results []; total still counts every match" \
  GET "/search?q=adventure&offset=1000000"
expect_status 200
expect_contains '"offset":1000000,'
expect_contains '"results":[]'
expect_total_positive
end_case

# --- GET /search: validation errors -----------------------------------------------------------

begin_case V1 "Missing q" "400 missing_parameter" \
  GET "/search"
expect_status 400
expect_contains '"error":"missing_parameter"'
expect_contains '\"q\"'
end_case

begin_case V2 "limit=0" "400 invalid_parameter naming limit" \
  GET "/search?q=adventure&limit=0"
expect_status 400
expect_contains '"error":"invalid_parameter"'
expect_contains 'limit'
end_case

begin_case V3 "limit=101" "400 invalid_parameter naming limit" \
  GET "/search?q=adventure&limit=101"
expect_status 400
expect_contains '"error":"invalid_parameter"'
expect_contains 'limit'
end_case

begin_case V4 "year=abc" "400 invalid_parameter naming year" \
  GET "/search?q=adventure&year=abc"
expect_status 400
expect_contains '"error":"invalid_parameter"'
expect_contains 'year'
end_case

# --- GET /books/{book_id} and GET /search/filters ---------------------------------------------

begin_case B1 "Book by ID" "200; metadata of book 84" \
  GET "/books/84"
expect_status 200
expect_contains '"book_id":84,'
expect_contains '"title":"Frankenstein'
end_case

begin_case B2 "Unknown book ID" "404 book_not_found" \
  GET "/books/99999999"
expect_status 404
expect_contains '"error":"book_not_found"'
end_case

begin_case B3 "Malformed book ID" "400 invalid_book_id" \
  GET "/books/abc"
expect_status 400
expect_contains '"error":"invalid_book_id"'
end_case

begin_case F1 "Filter values" "200; languages include en; years is an array" \
  GET "/search/filters"
expect_status 200
expect_contains '"languages":['
expect_contains '"en"'
expect_contains '"years":['
end_case
filters_body="$BODY"

# --- Contract choices of RF 1 (multi-word q) and RF 9 (CORS) ----------------------------------

begin_case X1 "Multi-word q is an AND" "200; only Frankenstein holds both words: 84 present, 82, 1341 and 1342 absent" \
  GET "/search?q=modern+prometheus"
expect_status 200
expect_contains '"book_id":84,'
expect_absent '"book_id":82,'
expect_absent '"book_id":1341,'
expect_absent '"book_id":1342,'
end_case

begin_case X2 "q without any word" "400 invalid_parameter naming q" \
  GET "/search?q=1813"
expect_status 400
expect_contains '"error":"invalid_parameter"'
expect_contains '\"q\"'
end_case

begin_case X3 "q made only of stop words" "200; total 0; results [] (stop words are never indexed)" \
  GET "/search?q=the"
expect_status 200
expect_contains '"total":0,'
expect_contains '"results":[]'
end_case

begin_case X4 "CORS preflight" "204 with Access-Control-Allow-Origin for the test origin" \
  OPTIONS "/search?q=whale" -H "Origin: $CORS_TEST_ORIGIN" -H "Access-Control-Request-Method: GET" \
  -H "Access-Control-Request-Headers: content-type"
expect_status 204
expect_header_in Access-Control-Allow-Origin '*' "$CORS_TEST_ORIGIN"
end_case

passed_count=$((case_count - failed_count))
echo "== $passed_count/$case_count checks passed =="

if [ -n "$record" ]; then
  revision=""
  if commit="$(git rev-parse --short HEAD 2>/dev/null)"; then
    revision=" at commit \`$commit\`"
    if [ -n "$(git status --porcelain --untracked-files=no 2>/dev/null || true)" ]; then
      revision="$revision plus uncommitted changes"
    fi
  fi
  {
    echo "# API validation — Search API contract (audit RF 10)"
    echo
    echo "Generated by \`scripts/api_validation.sh --record $record\` on $(date -u '+%Y-%m-%d %H:%M UTC') against \`$SEARCH_URL\`$revision."
    if [ -n "${STACK_DESCRIPTION:-}" ]; then
      echo "Stack: $STACK_DESCRIPTION"
    fi
    echo "Every response below is the literal body the service returned; regenerate this file with the same command."
    echo
    echo "**Result: $passed_count of $case_count checks passed.**"
    if [ -s "$workdir/seed.md" ]; then
      echo
      echo "## Corpus"
      echo
      echo "Seeded through the stack's own APIs before validating (both calls are idempotent):"
      echo
      echo "| Book | Ingestion Service | Indexing Service |"
      echo "|---|---|---|"
      cat "$workdir/seed.md"
    fi
    echo
    echo "## Summary"
    echo
    echo "| # | Case | Request | Expected | Status | Result |"
    echo "|---|---|---|---|---|---|"
    cat "$workdir/table.md"
    if [ "${year_total:-}" = 0 ] && grep -q '"years":\[\]' <<<"$filters_body"; then
      echo
      echo "> **Note on S4.** No published book has a known original publication year (\`GET /search/filters\`"
      echo "> returns \`\"years\":[]\`), so the year filter is verified for its contract only: it is applied and"
      echo "> echoed, and no result breaks it. This is the audit's known metadata finding (\`publication_year\` is 0"
      echo "> without an \"Original publication\" header line), not a Search API defect."
    fi
    echo
    echo "## Requests and responses"
    cat "$workdir/sections.md"
  } >"$record"
  echo "recorded $record"
fi

[ "$failed_count" -eq 0 ]
