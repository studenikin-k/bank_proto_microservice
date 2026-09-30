#!/bin/bash

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
cd "$SCRIPT_DIR/.."

echo "==================================="
echo "Bank Prototype - Load Testing Suite"
echo "==================================="
echo ""

mkdir -p load-tests/results

echo "[1/5] Running Smoke Test..."
k6 run load-tests/scenarios/smoke-test.js

echo ""
echo "[2/5] Running Load Test..."
k6 run load-tests/scenarios/load-test.js

echo ""
echo "[3/5] Running Stress Test..."
k6 run load-tests/scenarios/stress-test.js

echo ""
echo "[4/5] Running Spike Test..."
k6 run load-tests/scenarios/spike-test.js

echo ""
echo "[5/5] Running Full Scenario Test..."
k6 run load-tests/scenarios/full-scenario.js

echo ""
echo "==================================="
echo "All tests completed!"
echo "Results saved in load-tests/results/"
echo "==================================="

ls -lh load-tests/results/

