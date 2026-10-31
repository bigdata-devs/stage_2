#!/usr/bin/env bash
# Samples the CPU and memory of the Search Service replicas, nginx and MongoDB with `docker stats`
# while the k6 search benchmark runs (audit RF 5 and RF 8), writing one CSV row per container per
# sample. Run it in a second terminal, started just before k6 and stopped (Ctrl+C or DURATION) just
# after.
#
# Usage, from the repository root with the compose stack up:
#   test/load/sample_containers.sh                                   sample until Ctrl+C
#   test/load/sample_containers.sh test/load/results/search_resources.csv 200
#
# Arguments: OUT_FILE (default test/load/results/search_resources.csv, the folder must exist) and
# DURATION in seconds (default 0 = until Ctrl+C).
# Environment: CONTAINERS (default: every running container of the compose services search, nginx
# and mongo, so each replica of `--scale search=N` is sampled; outside the repository it falls back
# to "boogle-engine-search-1 boogle-engine-mongo-1") and INTERVAL (seconds slept between samples,
# default 1). Each `docker stats --no-stream` call itself takes about a second,
# so the effective period is about two seconds; every row carries its own UTC timestamp.
#
# cpu_percent is relative to one core (200 = two full cores), as docker stats reports it.
set -euo pipefail

OUT_FILE="${1:-test/load/results/search_resources.csv}"
DURATION="${2:-0}"
INTERVAL="${INTERVAL:-1}"
if [ -z "${CONTAINERS:-}" ]; then
  CONTAINERS="$(docker compose ps --format '{{.Name}}' search nginx mongo 2>/dev/null | tr '\n' ' ' || true)"
  [ -n "${CONTAINERS// /}" ] || CONTAINERS="boogle-engine-search-1 boogle-engine-mongo-1"
fi

read -r -a containers <<<"$CONTAINERS"
for container in "${containers[@]}"; do
  if ! docker inspect --format '{{.State.Running}}' "$container" 2>/dev/null | grep -q true; then
    echo "FAIL: container $container is not running; check the names with: docker compose ps" >&2
    exit 1
  fi
done

echo "timestamp_utc,container,cpu_percent,mem_used,mem_limit,mem_percent,pids" >"$OUT_FILE"
echo "sampling ${containers[*]} into $OUT_FILE every ~$((INTERVAL + 1))s$([ "$DURATION" -gt 0 ] && echo " for ${DURATION}s"); Ctrl+C stops"
deadline=$(($(date +%s) + DURATION))
samples=0
trap 'echo; echo "wrote $samples samples to $OUT_FILE"; exit 0' INT TERM
while [ "$DURATION" -eq 0 ] || [ "$(date +%s)" -lt "$deadline" ]; do
  timestamp="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  docker stats --no-stream --format '{{.Name}},{{.CPUPerc}},{{.MemUsage}},{{.MemPerc}},{{.PIDs}}' "${containers[@]}" |
    sed -e 's/%//g' -e 's# / #,#' -e "s/^/$timestamp,/" >>"$OUT_FILE"
  samples=$((samples + 1))
  sleep "$INTERVAL"
done
echo "wrote $samples samples to $OUT_FILE"
