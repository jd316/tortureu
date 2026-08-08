#!/bin/bash
# run.sh -- case 4's own runner, invoked by evals/run_case.sh instead of a
# plain `tortureu run` because this case needs two live dependencies
# (Redpanda/Pandaproxy, for -broker-url; Prometheus, for -prom-url) that a
# plain docker-compose service can never give the ORCHESTRATOR (a host
# process, not a container) reach to, once DC-2 topology enforcement moves
# them onto the internal:true SUT network.
#
# THE FINDING THIS SCRIPT WORKS AROUND (reported, not fixed -- internal/run
# is out of this task's scope): K6Runner was fixed (round 2 of this eval)
# to run k6 as a container joined to the SUT's own network namespace,
# sidestepping DC-2's "no host-published ports on an internal:true network"
# effect. internal/applier.BrokerApplier and internal/run.HTTPPromQuerier
# received no equivalent fix -- both are plain net/http calls made directly
# from the orchestrator's own host process, which cannot "join" a Docker
# network namespace the way a container can. So -broker-url and -prom-url,
# as documented CLI flags, cannot reach ANY target compose has moved onto
# the SUT network -- which, after DC-2 enforcement, is every compose
# service. This is the same defect class as round 1's original blocker,
# now shown to affect two more of the product's own host-side integrations
# that round 1 never exercised.
#
# The workaround: run Redpanda and Prometheus as containers THIS script
# manages directly, on a dedicated user-defined bridge network (so
# container-name DNS resolution works at all -- Docker's implicit default
# "bridge" network does not do this, unlike a user-defined one or the one
# docker-compose always creates; discovered the hard way while building
# this), each with a host-published port for this script's own reach, then
# additionally connected to tortureu_sut (with a DNS alias, for Redpanda)
# once that network exists, so checkout-api can reach them by the same
# container-name convention docker-compose would normally provide. This is
# test-harness engineering, not evidence the gap doesn't exist: a real
# user driving `tortureu run -broker-url ... -prom-url ...` against their
# own docker-compose.yml, with no such hand-built dual-homing, would see
# exactly the connection-refused/EOF errors this script works around.
set -u

CASE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TORTUREU_BIN="${1:?usage: run.sh <tortureu-binary>}"
OUT_FILE="${2:-/dev/stdout}"
ERR_FILE="${3:-/dev/stderr}"

REDPANDA_NAME="case4-redpanda-standalone"
PROM_NAME="case4-prometheus-standalone"
NET_NAME="case4-standalone-net"
REDPANDA_HOST_KAFKA_PORT=19092
REDPANDA_HOST_PROXY_PORT=19082
PROM_HOST_PORT=19099

cleanup() {
  docker rm -f "$REDPANDA_NAME" "$PROM_NAME" >/dev/null 2>&1
  local overlay="${TMPDIR:-/tmp}/tortureu-topology-overlay.yaml"
  if [ -f "$overlay" ]; then
    (cd "$CASE_DIR" && docker compose -f docker-compose.yml -f "$overlay" down -v) >/dev/null 2>&1
  else
    (cd "$CASE_DIR" && docker compose down -v) >/dev/null 2>&1
  fi
  # Same belt-and-suspenders as evals/run_case.sh: `docker compose down`
  # has been observed to leave the overlay's own proxy container running.
  docker ps -aq --filter "name=^$(basename "$CASE_DIR")-" 2>/dev/null | xargs -r docker rm -f >/dev/null 2>&1
  docker network ls --format '{{.Name}}' 2>/dev/null | grep -E '^tortureu_(sut|egress)$' | xargs -r docker network rm >/dev/null 2>&1
  docker network rm "$(basename "$CASE_DIR")_default" "$NET_NAME" >/dev/null 2>&1
}
trap cleanup EXIT

docker rm -f "$REDPANDA_NAME" "$PROM_NAME" >/dev/null 2>&1
docker network rm "$NET_NAME" >/dev/null 2>&1
docker network create "$NET_NAME" >/dev/null

echo "==> starting standalone Redpanda ($REDPANDA_NAME)" >&2
docker run -d --name "$REDPANDA_NAME" --network "$NET_NAME" --network-alias redpanda \
  -p "${REDPANDA_HOST_KAFKA_PORT}:9092" -p "${REDPANDA_HOST_PROXY_PORT}:8082" \
  redpandadata/redpanda:latest redpanda start --node-id=0 --smp=1 --memory=512M \
  --overprovisioned --check=false \
  --kafka-addr=PLAINTEXT://0.0.0.0:9092 --advertise-kafka-addr=PLAINTEXT://redpanda:9092 \
  --pandaproxy-addr=0.0.0.0:8082 --advertise-pandaproxy-addr=redpanda:8082 >/dev/null

echo "==> waiting for Redpanda and creating the 'orders' topic" >&2
for i in $(seq 1 60); do
  docker exec "$REDPANDA_NAME" rpk topic create orders --brokers redpanda:9092 >/dev/null 2>&1 && break
  docker exec "$REDPANDA_NAME" rpk topic list --brokers redpanda:9092 2>/dev/null | grep -q '^orders ' && break
  sleep 1
done
# Pandaproxy (a separate listener from the Kafka port rpk just proved
# ready) can take a few more seconds to start accepting HTTP connections
# even after the Kafka API answers -- confirmed empirically while building
# this fixture (an immediate produce attempt got a bare EOF, not a
# connection refused, i.e. the port was open but the HTTP server behind it
# wasn't yet). Poll its own REST endpoint directly rather than guessing a
# fixed sleep.
for i in $(seq 1 30); do
  curl -sf -m 2 "http://localhost:${REDPANDA_HOST_PROXY_PORT}/topics" >/dev/null 2>&1 && break
  sleep 1
done

echo "==> starting standalone Prometheus ($PROM_NAME)" >&2
docker run -d --name "$PROM_NAME" --network "$NET_NAME" -p "${PROM_HOST_PORT}:9090" \
  -v "$CASE_DIR/prometheus.yml:/etc/prometheus/prometheus.yml:ro" \
  prom/prometheus:latest >/dev/null

cd "$CASE_DIR" || exit 2
"$TORTUREU_BIN" run -config torture.yaml -json \
  -broker-url "http://localhost:${REDPANDA_HOST_PROXY_PORT}" \
  -prom-url "http://localhost:${PROM_HOST_PORT}" \
  >"$OUT_FILE" 2>"$ERR_FILE" &
RUN_PID=$!

# Once ComposeTopologyApplier creates tortureu_sut (early in Run(), before
# load starts), dual-home both standalone containers onto it -- Redpanda
# under the alias "redpanda" (what checkout-api's own env var names),
# Prometheus under no alias (it only needs to resolve outward, to
# "checkout-api", which its own membership on that network provides once
# it's connected). Polled in the foreground, in this same process, rather
# than a detached subshell -- a `( ... ) &` background job here was
# observed, while building this script, to leave OUT_FILE/ERR_FILE's
# descriptors open past the point where the caller expects this script to
# have finished, since it inherited them too.
for i in $(seq 1 60); do
  if docker network inspect tortureu_sut >/dev/null 2>&1; then
    docker network connect --alias redpanda tortureu_sut "$REDPANDA_NAME" >/dev/null 2>&1
    docker network connect tortureu_sut "$PROM_NAME" >/dev/null 2>&1
    break
  fi
  sleep 0.5
done

wait "$RUN_PID"
exit $?
