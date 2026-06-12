#!/usr/bin/env bash
set -euo pipefail

# Poseidon run.sh — supports two modes:
#
# Mode 1: Traditional (manager + worker directly)
#   ./run.sh traditional
#
# Mode 2: Self-hosted (agent with system containers)
#   ./run.sh self-hosted
#

MODE="${1:-traditional}"

case "$MODE" in
  traditional)
    echo "=== Starting Poseidon in traditional mode ==="
    # Start worker in background
    go run main.go worker --port 5556 --name worker-1 --dbtype memory &
    WORKER_PID=$!
    echo "Worker started (PID $WORKER_PID)"

    # Start manager (embedded Zodiac + raft backend)
    go run main.go manager \
      --port 5555 \
      --dbtype raft \
      --node-id 1 \
      --raft-port 9001 \
      --data-dir /tmp/poseidon/m1 \
      --workers localhost:5556 \
      --scheduler epvm \
      --service-cidr 10.42.0.0/16 &
    MANAGER_PID=$!
    echo "Manager started (PID $MANAGER_PID)"

    echo ""
    echo "Poseidon running. Press Ctrl+C to stop."
    echo ""
    echo "  Submit a task:  go run main.go run --filename data/task1.json"
    echo "  List tasks:     go run main.go status"
    echo "  List nodes:     go run main.go node"
    echo "  Stop a task:    go run main.go stop <uuid>"
    echo ""

    trap "kill $WORKER_PID $MANAGER_PID 2>/dev/null; exit" INT TERM
    wait
    ;;

  self-hosted)
    echo "=== Starting Poseidon in self-hosted mode ==="

    # Build Docker images for system components
    echo "Building Docker images..."
    docker build -t poseidon-worker:latest -f Dockerfile.worker . 2>&1 | tail -1
    docker build -t poseidon-proxy:latest -f Dockerfile.proxy . 2>&1 | tail -1
    docker build -t poseidon-dns:latest -f Dockerfile.dns . 2>&1 | tail -1
    echo "Docker images built."

    # Ensure manifest directory exists
    mkdir -p /tmp/poseidon/manifests
    cp manifests/*.json /tmp/poseidon/manifests/
    echo "Manifests copied to /tmp/poseidon/manifests/"

    # Start the agent (embedded Zodiac + system container manager)
    echo "Starting agent..."
    go run main.go agent \
      --node-id 1 \
      --raft-port 9001 \
      --data-dir /tmp/poseidon/agent \
      --manifest-dir /tmp/poseidon/manifests \
      --interval 10s &
    AGENT_PID=$!

    echo ""
    echo "Poseidon self-hosted mode running. Press Ctrl+C to stop."
    echo ""
    echo "  Agent PID: $AGENT_PID"
    echo "  Zodiac KV: http://localhost:9001"
    echo "  Manifests: /tmp/poseidon/manifests/"
    echo ""
    echo "  Run the manager separately to accept user tasks:"
    echo "    go run main.go manager --port 5555 --dbtype raft --node-id 1 --raft-port 9001"
    echo ""

    trap "kill $AGENT_PID 2>/dev/null; docker stop poseidon-worker poseidon-proxy poseidon-dns 2>/dev/null; exit" INT TERM
    wait
    ;;

  *)
    echo "Usage: $0 [traditional|self-hosted]"
    exit 1
    ;;
esac
