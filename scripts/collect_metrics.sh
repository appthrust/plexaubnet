#!/bin/bash
#
# Memory metrics & pprof collection script
# Usage: ./collect_metrics.sh [namespace]
#
# Description: Connects to the metrics endpoint of controller-manager Pod,
#              displays memory-related metrics in human-readable format,
#              and collects & compresses heap pprof profiles (60-second interval, max 240 files)
#

set -e

# Default settings
NAMESPACE=${1:-system}
METRICS_PORT=8080
METRICS_TARGET_PORT=8080
PPROF_PORT=6060
PPROF_TARGET_PORT=6060

echo "🔍 Searching for controller Pod in namespace ${NAMESPACE}..."

# Search for controller Pod
POD_NAME=$(kubectl get pods -n ${NAMESPACE} -l control-plane=controller-manager -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)

# Retry with name pattern if Pod not found
if [ -z "$POD_NAME" ]; then
  echo "  Pod not found with 'control-plane=controller-manager' label. Searching by name pattern..."
  POD_NAME=$(kubectl get pods -n ${NAMESPACE} | grep -i 'controller-manager' | head -n 1 | awk '{print $1}')
fi

if [ -z "$POD_NAME" ]; then
  echo "❌ No controller Pod found in namespace ${NAMESPACE}!"
  echo "   Please specify the correct namespace. Example: ./collect_metrics.sh aquanaut-system"
  exit 1
fi

echo "✅ Controller Pod: ${POD_NAME}"

# Port-forward setup
echo "🔄 Setting up port-forward..."
echo "  - metrics: ${METRICS_PORT}:${METRICS_TARGET_PORT}"
echo "  - pprof: ${PPROF_PORT}:${PPROF_TARGET_PORT}"

kubectl port-forward -n ${NAMESPACE} ${POD_NAME} ${METRICS_PORT}:${METRICS_TARGET_PORT} ${PPROF_PORT}:${PPROF_TARGET_PORT} > /dev/null 2>&1 &
PF_PID=$!

# pprof collection function
function pprof_collector() {
  mkdir -p heap
  echo "📊 Started pprof heap collection (30-second interval, keeping max 240 files)"
  while true; do
    ts=$(date +%s)
    curl -sS http://localhost:${PPROF_PORT}/debug/pprof/heap -o heap/heap_${ts}.prof 2>/dev/null &&
      xz -T0 heap/heap_${ts}.prof
    # Keep only the latest 240 files (30-second interval for ~2 hours)
    ls -1t heap/heap_*.prof.xz | tail -n +241 | xargs -r rm -f
    sleep 30
  done
}

# Stop port-forward and pprof collector on exit
function cleanup {
  echo "🧹 Terminating background processes..."
  echo "  - port-forward (PID: ${PF_PID})"
  kill ${PF_PID} 2>/dev/null || true
  if [ -n "$PPROF_PID" ]; then
    echo "  - pprof collector (PID: ${PPROF_PID})"
    kill ${PPROF_PID} 2>/dev/null || true
  fi
  echo "  pprof collection results are saved in heap/ directory"
}
trap cleanup EXIT

# Wait for port-forward to be ready and start pprof collector
echo "⏳ Waiting for port-forward to be ready..."
sleep 2

# Start pprof collection
pprof_collector &
PPROF_PID=$!

# Get metrics
echo "📊 Retrieving memory metrics..."
METRICS=$(curl -s http://localhost:${METRICS_PORT}/metrics)

# Extract important memory metrics (excluding HELP & TYPE comment lines)
RESIDENT_MEM=$(echo "${METRICS}" | grep -E '^process_resident_memory_bytes [0-9]' | awk '{print $2}')
HEAP_INUSE=$(echo "${METRICS}" | grep -E '^go_memstats_heap_inuse_bytes [0-9]' | awk '{print $2}')
HEAP_ALLOC=$(echo "${METRICS}" | grep -E '^go_memstats_alloc_bytes [0-9]' | awk '{print $2}')
HEAP_OBJECTS=$(echo "${METRICS}" | grep -E '^go_memstats_heap_objects [0-9]' | awk '{print $2}')

# Fallback if values couldn't be extracted correctly
if [ -z "$RESIDENT_MEM" ]; then
  echo "⚠️ Warning: Problem with metrics extraction. Trying alternative extraction method..."
  RESIDENT_MEM=$(echo "${METRICS}" | grep -A1 'process_resident_memory_bytes' | grep -v 'HELP\|TYPE' | awk '{print $2}')
  HEAP_INUSE=$(echo "${METRICS}" | grep -A1 'go_memstats_heap_inuse_bytes' | grep -v 'HELP\|TYPE' | awk '{print $2}')
  HEAP_ALLOC=$(echo "${METRICS}" | grep -A1 'go_memstats_alloc_bytes' | grep -v 'HELP\|TYPE' | awk '{print $2}')
  HEAP_OBJECTS=$(echo "${METRICS}" | grep -A1 'go_memstats_heap_objects' | grep -v 'HELP\|TYPE' | awk '{print $2}')
fi

# Convert memory to human-readable format
function human_bytes {
  local bytes=$1
  if [ -z "$bytes" ] || [ "$bytes" = "0" ]; then
    echo "Unknown"
    return
  fi
  
  # Handle exponential notation (1.2e+08)
  local numeric_bytes=$bytes
  if [[ $bytes == *e* ]]; then
    # Convert for bc to handle exponential notation
    numeric_bytes=$(printf "%.0f" $bytes)
  fi
  
  local mb=$(echo "scale=1; ${numeric_bytes} / 1024 / 1024" | bc 2>/dev/null || echo "Calculation error")
  local percent=$(echo "scale=1; ${mb} / 256 * 100" | bc 2>/dev/null || echo "Calculation error")
  echo "${mb}MB (${percent}%)"
}

# Count resources
echo "🔢 Counting resources..."
SUBNET_COUNT=$(kubectl get subnet --all-namespaces --no-headers 2>/dev/null | wc -l | xargs)
SUBNETCLAIM_COUNT=$(kubectl get subnetclaim --all-namespaces --no-headers 2>/dev/null | wc -l | xargs)
SUBNETPOOL_COUNT=$(kubectl get subnetpool --all-namespaces --no-headers 2>/dev/null | wc -l | xargs)
TOTAL_RESOURCES=$((SUBNET_COUNT + SUBNETCLAIM_COUNT + SUBNETPOOL_COUNT))

# Display report
echo ""
echo "===================================================="
echo "📝 Memory Usage Report - $(date '+%Y-%m-%d %H:%M:%S') (pprof collection in progress)"
echo "===================================================="
echo "🏷️ Pod: ${POD_NAME} (Namespace: ${NAMESPACE})"
echo ""
echo "📈 Memory Usage:"
echo "  - Resident Memory:   $(human_bytes ${RESIDENT_MEM})"
echo "  - Heap In-Use:       $(human_bytes ${HEAP_INUSE})"
echo "  - Heap Allocated:    $(human_bytes ${HEAP_ALLOC})"
echo "  - Heap Objects:      ${HEAP_OBJECTS} objects"
echo ""
echo "🔢 Resource Statistics:"
echo "  - SubnetClaim:       ${SUBNETCLAIM_COUNT} items"
echo "  - Subnet:            ${SUBNET_COUNT} items"
echo "  - SubnetPool:        ${SUBNETPOOL_COUNT} items"
echo "  - Total Resources:   ${TOTAL_RESOURCES} items"
echo ""
echo "🧠 Memory Efficiency:"
if [ -n "$RESIDENT_MEM" ] && [ "$TOTAL_RESOURCES" -gt 0 ]; then
  # Handle exponential notation
  numeric_bytes=$RESIDENT_MEM
  if [[ $RESIDENT_MEM == *e* ]]; then
    numeric_bytes=$(printf "%.0f" $RESIDENT_MEM)
  fi
  
  kb_per_resource=$(echo "scale=2; ${numeric_bytes} / ${TOTAL_RESOURCES} / 1024" | bc 2>/dev/null || echo "Calculation error")
  echo "  - Per Resource:      ${kb_per_resource} KB/resource"
else
  echo "  - Per Resource:      Could not calculate"
fi
echo "===================================================="
echo ""
echo "📋 Markdown Table Format (for copying):"
echo ""
echo "| Metric | Value | Approx | % of Limit |"
echo "|--------|-------|--------|------------|"

# Function to handle exponential notation
function format_metric {
  local value=$1
  local divisor=$2
  local suffix=$3
  
  if [ -z "$value" ]; then
    echo "Unknown"
    return
  fi
  
  # Handle exponential notation
  local numeric_value=$value
  if [[ $value == *e* ]]; then
    numeric_value=$(printf "%.0f" $value)
  fi
  
  local result=$(echo "scale=1; ${numeric_value} ${divisor}" | bc 2>/dev/null || echo "Calculation error")
  echo "${result}${suffix}"
}

# Display memory metrics
if [ -n "$RESIDENT_MEM" ]; then
  mem_mb=$(format_metric "$RESIDENT_MEM" "/ 1024 / 1024" "")
  mem_percent=$(format_metric "$RESIDENT_MEM" "/ 1024 / 1024 / 256 * 100" "")
  echo "| process_resident_memory_bytes | ${RESIDENT_MEM} | ≈ ${mem_mb} MB | ${mem_percent}% |"
else
  echo "| process_resident_memory_bytes | Unknown | - | - |"
fi

if [ -n "$HEAP_INUSE" ]; then
  heap_mb=$(format_metric "$HEAP_INUSE" "/ 1024 / 1024" "")
  heap_percent=$(format_metric "$HEAP_INUSE" "/ 1024 / 1024 / 256 * 100" "")
  echo "| go_memstats_heap_inuse_bytes | ${HEAP_INUSE} | ≈ ${heap_mb} MB | ${heap_percent}% |"
else
  echo "| go_memstats_heap_inuse_bytes | Unknown | - | - |"
fi

if [ -n "$HEAP_ALLOC" ]; then
  alloc_mb=$(format_metric "$HEAP_ALLOC" "/ 1024 / 1024" "")
  alloc_percent=$(format_metric "$HEAP_ALLOC" "/ 1024 / 1024 / 256 * 100" "")
  echo "| go_memstats_alloc_bytes | ${HEAP_ALLOC} | ≈ ${alloc_mb} MB | ${alloc_percent}% |"
else
  echo "| go_memstats_alloc_bytes | Unknown | - | - |"
fi

if [ -n "$HEAP_OBJECTS" ]; then
  objects_man=$(format_metric "$HEAP_OBJECTS" "/ 10000" "")
  echo "| go_memstats_heap_objects | ${HEAP_OBJECTS} | ${objects_man}0K | - |"
else
  echo "| go_memstats_heap_objects | Unknown | - | - |"
fi
echo ""
echo "Resource count: SubnetClaim=${SUBNETCLAIM_COUNT}, Subnet=${SUBNET_COUNT}, SubnetPool=${SUBNETPOOL_COUNT}, Total=${TOTAL_RESOURCES}"