# Heap Profile Analysis Guide

This directory contains heap profiles (`.prof`, `.prof.xz`) automatically collected by `scripts/collect_metrics.sh` and `scripts/run_soak.sh`. The following explains how to analyze these profiles.

## 1. Basic Analysis Commands

Pprof files can be analyzed with Go's standard tool `go tool pprof`:

```bash
# Display top 10 memory usage locations in text format
go tool pprof -top heap/heap_1746756315.prof

# Tree view of memory allocating functions
go tool pprof -tree heap/heap_1746756315.prof

# Detailed profile display of specific function
go tool pprof -list='FuncName' heap/heap_1746756315.prof

# Launch interactive shell (capable of executing detailed commands)
go tool pprof heap/heap_1746756315.prof
# Commands available at prompt: top, list, web, etc.
```

## 2. Browser Visualization (Recommended)

The most user-friendly analysis method is the browser-based interface:

```bash
# Launch graphical interface in browser
go tool pprof -http=:8000 heap/heap_1746756315.prof
```

In the browser (http://localhost:8000), the following analyses are available:

- **Flame Graph**: Hierarchical visualization of memory allocations
- **Top**: Ranking of functions consuming the most memory
- **Source**: Mapping of functions to source code
- **Graph**: Relationship diagram of function calls and memory allocations

## 3. Differential Analysis of Multiple Profiles

For comparing before/after optimization or changes over time:

```bash
# Compare two profiles (before and after optimization, etc.)
go tool pprof -http=:8000 -base=heap/heap_baseline.prof heap/heap_after_patch.prof

# Display differences in command line output
go tool pprof -top -diff_base=heap/heap_baseline.prof heap/heap_after_patch.prof
```

## 4. Focus Points for P4/P5 Optimization Verification

For validating Memory Optimization Plan's P4 (Map pre-allocation) and P5 (GOGC=80):

```bash
# Check the effect of map pre-allocation (P4)
go tool pprof -http=:8000 -focus='FreeCountBySize|make.map' heap/heap_1746756315.prof

# Focus on specific controller
go tool pprof -http=:8000 -focus='PoolStatusReconciler' heap/heap_1746756315.prof

# Check the effect of GC settings (P5) (comparing multiple files)
go tool pprof -http=:8000 -base=heap/before_p5.prof heap/after_p5.prof
```

## 5. Long-Run Leak Detection and Growth Rate Analysis

```bash
# Analysis of multiple profiles in time series
for f in $(ls -t heap/heap_*.prof | head -5); do 
  echo "==== $f ===="; 
  go tool pprof -top -lines -inuse_space $f | head -15; 
  echo; 
done

# Direct analysis of compressed files (.xz)
go tool pprof -http=:8000 heap/heap_12345.prof.xz
```

## 6. Output Analysis Results and PDF Generation

For report creation and documentation:

```bash
# Generate PDF of heap profile analysis
go tool pprof -pdf -output=heap_analysis.pdf heap/heap_1746756315.prof

# Generate PDF of Before/After differences (for P4P5 effect verification)
go tool pprof -pdf -output=heap_diff.pdf -base=heap/before_p4p5.prof heap/after_p4p5.prof
```

## 7. Other Useful pprof Options

```bash
# Switch measurement targets (inuse_space, alloc_space, etc.)
go tool pprof -sample_index=alloc_space -http=:8000 heap/heap_1746756315.prof

# Export in SVG format
go tool pprof -svg -output=heap.svg heap/heap_1746756315.prof

# Display profile summary
go tool pprof -traces heap/heap_1746756315.prof
```

## 8. Automated Analysis Script Example

You can automate analysis using `scripts/analyze_heap.sh` as follows:

```bash
#!/bin/bash
HEAP_FILE=$1
OUTPUT_DIR="heap_analysis_$(date +%Y%m%d_%H%M)"

mkdir -p $OUTPUT_DIR

echo "Running basic analysis..."
go tool pprof -text -top -lines -output=$OUTPUT_DIR/top_allocations.txt $HEAP_FILE

echo "Running map-related analysis..."
go tool pprof -text -focus='map.*|Pool.*|Subnet.*' -output=$OUTPUT_DIR/map_allocations.txt $HEAP_FILE

echo "Generating PDF..."
go tool pprof -pdf -output=$OUTPUT_DIR/heap.pdf $HEAP_FILE

echo "Analysis results: $OUTPUT_DIR"
```

## Notes

- Profiles may contain information that impacts production environment security, so please review content before sharing
- Large profiles may take time to analyze
- GC-related optimizations such as GOGC=80 settings may have different effects depending on the load conditions