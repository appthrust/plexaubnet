#!/usr/bin/env bash

# Heap dump analysis script
# Usage: ./scripts/analyze_heap.sh [log directory]

set -e

# Default log directory
LOG_DIR=${1:-"./logs"}
HEAP_DIR="${LOG_DIR}/heap"

if [ ! -d "${HEAP_DIR}" ]; then
  echo "❌ Error: Heap dump directory not found: ${HEAP_DIR}"
  exit 1
fi

# List all collected heap dumps in chronological order
echo "📊 Heap dump file list:"
HEAP_FILES=($(ls -t ${HEAP_DIR}/heap-*.pprof 2>/dev/null || echo ""))

if [ ${#HEAP_FILES[@]} -eq 0 ]; then
  echo "❌ Error: No heap dump files found"
  exit 1
fi

echo "🔍 Found a total of ${#HEAP_FILES[@]} heap dumps"
FIRST_DUMP=${HEAP_FILES[-1]}
LAST_DUMP=${HEAP_FILES[0]}

echo "📈 First dump: $(basename ${FIRST_DUMP})"
echo "📉 Last dump: $(basename ${LAST_DUMP})"

# Differential analysis between baseline (first) and final dump
echo "🔄 Running differential analysis..."
echo "-----------------------------------"
echo "💾 Memory usage Top 10 (inuse_space)"
echo "-----------------------------------"
go tool pprof -top -inuse_space -lines -diff_base=${FIRST_DUMP} ${LAST_DUMP}

echo ""
echo "-----------------------------------"
echo "🧮 Object count Top 10 (alloc_objects)"
echo "-----------------------------------"
go tool pprof -top -alloc_objects -lines -diff_base=${FIRST_DUMP} ${LAST_DUMP}

echo ""
echo "💡 For detailed analysis run:"
echo "go tool pprof -http=:8080 ${LAST_DUMP}  # Web server display"
echo "go tool pprof -flame ${LAST_DUMP}       # Generate flame graph"
echo "go tool pprof -svg -output=heap.svg ${LAST_DUMP}  # SVG output"

# Determine optimization targets
echo ""
echo "🎯 Recommended optimization targets:"
TOP_SPACE=$(go tool pprof -top -inuse_space -lines -diff_base=${FIRST_DUMP} ${LAST_DUMP} | grep -v 'runtime\|Total' | head -5)

if echo "$TOP_SPACE" | grep -q "DeepCopy"; then
  echo "✅ OPT-1: Reduce DeepCopy() chains (copy reduction)"
fi

if echo "$TOP_SPACE" | grep -q "make.*map"; then
  echo "✅ OPT-2: Optimize map pre-allocation capacity"
fi

if echo "$TOP_SPACE" | grep -q "List\|client.List"; then
  echo "✅ OPT-3: List scope filtering (add Namespace filter)"
fi

if echo "$TOP_SPACE" | grep -q "[mM]ap\|sync.Map"; then
  echo "✅ OPT-4: Implement Cache Purge mechanism"
fi

echo "✅ OPT-5: Investigate GC configuration adjustments (GOGC=80)"