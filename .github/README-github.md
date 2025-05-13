# GitHub Actions Workflows

This directory contains GitHub Actions workflows for CI/CD pipelines.

## Workflow Overview

### Race Detection (`race.yml`)
- **Purpose**: Run Go data race detection tests
- **Triggers**: `push`, `pull_request` 
- **Execution environment**: amd64, arm64
- **Features**:
  - Run tests with `-race` flag on amd64
  - Static analysis only on arm64 (`go vet` + minimal compilation)
  - Basic quality check before PR review

### Load Testing (`soak.yml`)
- **Purpose**: Run load tests using k6 + Kind
- **Triggers**:
  - Scheduled execution (Nightly) - UTC 18:00 (JST 03:00)
  - Manual execution (two execution modes)
- **Test Types**:
  - **Smoke Test** (5 min): Basic verification for manual execution
  - **Nightly Soak** (15 min): Memory leak detection for scheduled execution
  - **Full Soak** (24 hours): Complete load test for manual execution (when `soak=true` is specified)

## Common Components

### Setup Go & Envtest Action
- **File**: `.github/actions/setup-go-envtest/action.yml`
- **Function**: Sets up Go environment and controller-runtime envtest
- **Parameters**:
  - `go-version`: Go version (default: 1.24.2)
  - `envtest-version`: Kubernetes version (default: 1.32)

### Setup K6 & Kind Action
- **File**: `.github/actions/setup-k6-kind/action.yml`
- **Function**: Sets up K6 load testing tool and Kind
- **Parameters**:
  - `kind-version`: Kind version (default: v0.27.0)
  - `k6-version`: K6 version (default: latest)
  - `kind-url`: Custom URL for Kind binary (optional)

## Manual Execution

### Manual Execution of Race Detection
1. Select the "Actions" tab in the GitHub repository
2. Select "Race Detection" workflow from the left
3. Click the "Run workflow" button
4. Select branch as needed and run

### Manual Execution of Load Testing
1. Select the "Actions" tab in the GitHub repository
2. Select "Soak & Load Testing" workflow from the left
3. Click the "Run workflow" button
4. Execution options:
   - Normal execution (5 min Smoke Test): Run with default settings
   - 24 hour Full Soak Test: Select "true" for "Run full 24h soak test"
5. Click the "Run workflow" button

## Troubleshooting

### Cache-related Issues
- If envtest binaries are being re-downloaded, check the cache key
- When updating to a new Kubernetes version, cache will be created on first run

### arm64 Build Failures
- Tests for arm64 are compiled only but not executed, so errors are from static analysis
- Fix by addressing `go vet` errors

### Long Test Interruptions
- 24-hour tests may hit GitHub Actions execution time limits (6 hours)
- Consider using self-hosted runners or splitting into multiple stages if this occurs