#!/usr/bin/env bash
# Samples the whole host — the machine that runs Docker Engine and k6 — while a benchmark runs,
# complementing test/load/sample_containers.sh (per-container) with the machine totals the Stage 2
# spec (§6.4/§7) asks for: CPU busy percentage, load average and memory used. One CSV row per
# sample. In this deployment the host is the WSL2 distro that runs Docker Engine 29 and k6 (12
# vCPUs, 9.7 GB); container samples cover the services, this sampler covers everything else k6,
# Docker and the OS included.
#
# Usage, from the repository root, started just before k6 and stopped (Ctrl+C or DURATION) just
# after:
#   test/load/sample_host.sh                                    # until Ctrl+C
#   test/load/sample_host.sh docs/evidence/host_indexing.csv 90 # for 90 seconds
#
# Arguments: OUT_FILE (default docs/evidence/host_resources.csv) and DURATION in seconds
# (default 0 = until Ctrl+C). Environment: INTERVAL (seconds between samples, default 2).
# cpu_busy_pct is the share of non-idle time over the whole machine (100 = all 12 vCPUs busy),
# derived from the deltas of /proc/stat between two samples.
set -euo pipefail

OUT_FILE="${1:-docs/evidence/host_resources.csv}"
DURATION="${2:-0}"
INTERVAL="${INTERVAL:-2}"

read_cpu() {
  # "user nice system idle iowait irq softirq steal" -> "<total> <idle+iowait>"
  awk '/^cpu /{print ($2+$3+$4+$5+$6+$7+$8+$9), ($5+$6)}' /proc/stat
}

read_load() {
  awk '{print $1}' /proc/loadavg
}

read_memory_mb() {
  awk '/MemTotal:/{t=$2} /MemAvailable:/{a=$2} END{printf "%d %d", (t-a)/1024, t/1024}' /proc/meminfo
}

previous_cpu="$(read_cpu)"
echo "timestamp_utc,cpu_busy_pct,load1,mem_used_mb,mem_total_mb" >"$OUT_FILE"
echo "sampling host CPU/load/memory into $OUT_FILE every ${INTERVAL}s$([ "$DURATION" -gt 0 ] && echo " for ${DURATION}s"); Ctrl+C stops"
deadline=$(($(date +%s) + DURATION))
samples=0
trap 'echo; echo "wrote $samples samples to $OUT_FILE"; exit 0' INT TERM
while [ "$DURATION" -eq 0 ] || [ "$(date +%s)" -lt "$deadline" ]; do
  sleep "$INTERVAL"
  current_cpu="$(read_cpu)"
  cpu_busy_pct="$(awk -v p="$previous_cpu" -v c="$current_cpu" 'BEGIN{
    split(p, pf, " "); split(c, cf, " ");
    total = cf[1] - pf[1]; idle = cf[2] - pf[2];
    if (total <= 0) { print 0 } else { printf "%.1f", 100 * (total - idle) / total }
  }')"
  previous_cpu="$current_cpu"
  read -r mem_used_mb mem_total_mb <<<"$(read_memory_mb)"
  timestamp="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  echo "$timestamp,$cpu_busy_pct,$(read_load),$mem_used_mb,$mem_total_mb" >>"$OUT_FILE"
  samples=$((samples + 1))
done
echo "wrote $samples samples to $OUT_FILE"
