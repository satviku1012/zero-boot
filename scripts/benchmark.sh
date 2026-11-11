#!/usr/bin/env bash
# benchmark.sh: drive a running ZeroBoot cluster (docker compose up) and
# report allocation latency for warm hits vs cold starts.
#
# Phases:
#   1. warm   5 rapid sequential requests       -> WARM_HIT, < 5ms allocation
#   2. burst  3 concurrent requests right after  -> pool empty, COLD_MISS ~450ms
#   3. hang   1 request with ?simulate_hang=true -> lease reclaimed by the 10s TTL
#   4. dist   SAMPLES paced requests             -> warm latency distribution
#   5. bursts BURSTS x 10 concurrent requests    -> 5 warm + 5 cold each time
#
# Every request lands in $OUT/results.csv; scripts/plot_benchmark.py turns
# that file into the README charts. Needs curl, jq and docker (for logs).
# Written for bash 3.2, so it runs on stock macOS.
set -euo pipefail
cd "$(dirname "$0")/.."

GATEWAY=${GATEWAY:-http://localhost:${GATEWAY_PORT:-8080}}
SAMPLES=${SAMPLES:-1000} # phase 4 request count
INTERVAL=${INTERVAL:-0.15} # seconds between phase 4 requests (slower than refill)
BURSTS=${BURSTS:-20}     # phase 5 burst count
BURST_SIZE=10            # twice the pool size of 5
OUT=${OUT:-bench-results}

for cmd in curl jq docker; do
  command -v "$cmd" >/dev/null || { echo "benchmark: $cmd is required" >&2; exit 1; }
done
mkdir -p "$OUT"
CSV=$OUT/results.csv
echo "phase,status,alloc_ms,total_ms,node,sandbox_id" >"$CSV"
FAILED=0

# request PHASE [QUERY]: POST one task, append a CSV row, print the row.
# total_ms is curl's view of the whole HTTP request (claim + TCP + release).
request() {
  local phase=$1 query=${2:-} resp body total row
  resp=$(curl -s -w $'\n%{time_total}' -X POST "$GATEWAY/execute$query" \
    -d "{\"payload\":\"$phase task\"}")
  body=${resp%$'\n'*}
  total=${resp##*$'\n'}
  row=$(jq -r --arg p "$phase" --arg t "$total" \
    '[$p, (.status // "ERROR"), (.allocation_latency_ms // 0),
      (($t | tonumber) * 1000 | . * 1000 | round / 1000),
      (.node // ""), (.sandbox_id // "")] | map(tostring) | join(",")' <<<"$body")
  echo "$row" >>"$CSV" # one short line: a single atomic append, safe in parallel
  echo "$row"
}

# show: pretty-print CSV rows from stdin.
show() { awk -F, '{ printf "  %-7s %-9s alloc %8.3f ms   total %8.3f ms   %s %s\n", $1, $2, $3, $4, $5, $6 }'; }

# check DESCRIPTION CONDITION...: print PASS/FAIL for a shell condition.
check() {
  local desc=$1; shift
  if "$@"; then echo "  PASS  $desc"; else echo "  FAIL  $desc"; FAILED=1; fi
}

# phase_ok PHASE STATUS MIN MAX: every PHASE row has STATUS and MIN <= alloc < MAX.
phase_ok() {
  awk -F, -v p="$1" -v s="$2" -v lo="$3" -v hi="$4" \
    '$1 == p { n++; if ($2 != s || $3 < lo || $3 >= hi) bad++ } END { exit !(n > 0 && !bad) }' "$CSV"
}

echo "Waiting for $GATEWAY ..."
tries=0
until curl -sf -X POST "$GATEWAY/execute" -d '{"payload":"warmup"}' >/dev/null; do
  tries=$((tries + 1))
  [ "$tries" -lt 30 ] || { echo "gateway not reachable; run: docker compose up --build -d" >&2; exit 1; }
  sleep 1
done
# The warm-up opened the gateway -> controller connection (a one-time ~200ms
# cost) and is not recorded. Give the pool time to refill to 5/5.
sleep 3

echo; echo "1) 5 rapid warm requests"
for i in 1 2 3 4 5; do request warm; done | show
check "all 5 are WARM_HIT with allocation < 5ms" phase_ok warm WARM_HIT 0 5

echo; echo "2) 3 burst requests beyond pool capacity"
for i in 1 2 3; do request burst & done | show
wait
check "all 3 are COLD_MISS with allocation 400-1000ms" phase_ok burst COLD_MISS 400 1000

echo; echo "3) 1 hanging request (?simulate_hang=true), never released"
sleep 3
id=$(request hang "?simulate_hang=true" | tee >(show >&2) | cut -d, -f6)
reclaimed=""
for i in $(seq 20); do
  reclaimed=$(docker compose logs controller 2>/dev/null |
    sed -n "s/.*lease $id expired after \([0-9.]*\)s.*/\1/p" | tail -1)
  [ -n "$reclaimed" ] && break
  sleep 1
done
echo "  lease $id reclaimed by TTL cleanup after ${reclaimed:-?}s"
check "hung lease destroyed between 10s and 13s" \
  awk -v t="${reclaimed:-0}" 'BEGIN { exit !(t >= 10 && t < 13) }'

echo; echo "4) $SAMPLES paced requests, ${INTERVAL}s apart (latency distribution)"
for i in $(seq "$SAMPLES"); do
  request dist >/dev/null
  [ $((i % 100)) -eq 0 ] && echo "  $i/$SAMPLES"
  sleep "$INTERVAL"
done

echo; echo "5) $BURSTS bursts of $BURST_SIZE concurrent requests (pool size 5)"
for b in $(seq "$BURSTS"); do
  sleep 3 # refill to 5/5 before each burst
  for i in $(seq "$BURST_SIZE"); do request bursts >/dev/null & done
  wait
done
echo "  done"

# Nearest-rank percentiles per phase and status.
echo; echo "Allocation latency (ms)"
printf "  %-7s %-9s %6s %8s %8s %8s %8s\n" phase status n p50 p95 p99 max
tail -n +2 "$CSV" | sort -t, -k1,1 -k2,2 -k3,3n | awk -F, '
  function report(   i, r) {
    if (!n) return
    printf "  %-7s %-9s %6d %8.3f %8.3f %8.3f %8.3f\n", key1, key2, n,
      v[int(n*0.50 + 0.999)], v[int(n*0.95 + 0.999)], v[int(n*0.99 + 0.999)], v[n]
  }
  $1 != key1 || $2 != key2 { report(); key1 = $1; key2 = $2; n = 0 }
  { v[++n] = $3 }
  END { report() }'

echo; echo "Isolation"
on=$(docker compose logs node-1 node-2 2>/dev/null | grep -c "cgroup=true" || true)
off=$(docker compose logs node-1 node-2 2>/dev/null | grep -c "cgroup=false" || true)
echo "  sandboxes in a cgroup: $on, signal fallback: $off"
check "every sandbox used a real cgroup" [ "$on" -gt 0 -a "$off" -eq 0 ]
errors=$(awk -F, '$2 == "ERROR"' "$CSV" | wc -l | tr -d ' ')
check "no failed requests ($errors errors)" [ "$errors" -eq 0 ]

echo; echo "Raw results: $CSV"
exit "$FAILED"
