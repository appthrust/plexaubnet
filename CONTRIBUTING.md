# Contributing to Plexaubnet

Welcome and thank you for taking the time to contribute!  
This guide explains the workflow, coding standards, and policies that keep the project healthy and collaborative.

## 1. Project Overview

Plexaubnet is a **Kubernetes-native IP Address Management (IPAM) controller set** written in Go.  
The project follows the [Apache License 2.0](./LICENSE) and embraces open, friendly collaboration.

---

## 2. Development Environment

### 2.1 Required tools

| Tool | Minimum Version | Purpose |
|------|-----------------|---------|
| Go   | 1.24            | Build & test |
| make | *latest*        | Task automation |
| Docker | 24.x          | Local container runtime |
| kind | 0.22            | Local Kubernetes cluster |
| Helm | 3.14 (optional) | Chart testing |

### 2.2 Initial setup

```bash
# Clone
git clone https://github.com/appthrust/plexaubnet.git
cd plexaubnet

# Install dependencies & pre-commit hooks
make deps

# Start local Kubernetes cluster
make kind-up      # kind create cluster + local registry
make install      # install CRDs into the cluster
```

---

## 3. Branch Strategy

* `main` – always **releasable**; protected branch  
* `feature/<short-desc>` – active development  
* `release/vX.Y.Z` – release preparation  
* `hotfix/<issue>` – emergency patch

Create a feature branch, commit your changes, then open a Pull Request (PR).

---

## 4. Coding Guidelines

* Run **`go fmt ./...`**, **`go vet ./...`**, and **`golangci-lint run`** before pushing.  
* Keep package names lowercase and short (`allocator`, `netutil`).  
* Follow existing file layout (`internal/controller`, `docs/design`).  
* Add unit tests for new functionality.  
* Update **docs/** when behavior or API changes.

---

## 5. Commit Messages & Pull Requests

### 5.1 Conventional Commits

Prefix commits with a type:

| Type | Description |
|------|-------------|
| `feat` | New feature |
| `fix`  | Bug fix |
| `docs` | Documentation only |
| `test` | Adding or updating tests |
| `chore`| Build/CI, refactors, misc |

Example:

```
feat(controller): add subnet allocator
```

### 5.2 Pull Request Checklist

* Overview – *why* this change is needed  
* Changes – bullet list of key modifications  
* Tests – how to verify  
* Impact – migration or compatibility notes  
* Checklist – [ ] code formatted, [ ] tests pass, [ ] docs updated

---

## 6. Tests & Continuous Integration

Command | Purpose
--------|---------
`make test` | Run unit tests
`make e2e`  | Envtest + kind integration tests
`make lint` | Execute golangci-lint
`make coverage` | Generate coverage report

All checks run automatically in **GitHub Actions** on every PR.

---

## 7. Documentation Updates

Design documents live under **docs/design/**.  
If your change adds a new controller, CRD, or significant behavior, include/update a design doc.

---

## 8. Issue & Discussion Guidelines

When opening an issue, please include:

1. **Steps to reproduce**  
2. **Expected behavior**  
3. **Actual behavior / logs**  
4. **Environment** (Go version, Kubernetes version)

Use **GitHub Discussions** for questions and proposals.

---

## 9. License Compliance

The project is licensed under Apache-2.0.  
When adding dependencies, ensure they are **Apache-2.0, MIT, BSD, or similarly permissive**.  
Run:

```bash
go-licenses csv ./... | grep -v "UNKNOWN"
```

and attach the report in your PR if new licenses appear.

---

## 10. Developer Certificate of Origin (DCO)

All commits must be signed:

```bash
git commit -s -m "feat: awesome contribution"
```

which appends:

```
Signed-off-by: Your Name <email@example.com>
```

---

## 11. Becoming a Maintainer

* Submit at least **3 high-quality PRs** reviewed & merged.  
* Actively participate in reviews and discussions.  
* Request maintainer status via a Discussion thread; existing maintainers vote.

---

## Contribution Flow (Mermaid)

```mermaid
flowchart TD
    A[Fork repository] --> B[Clone locally]
    B --> C[Create feature/* branch]
    C --> D[Code & Commit<br/>(Signed-off-by)]
    D --> E[Run Lint & Tests]
    E --> F[Open Pull Request]
    F --> G[CI Checks]
    G --> H[Code Review]
    H --> I[Merge to main]
```

---

Happy hacking! We look forward to your contributions.  
If anything is unclear, open an issue or start a discussion.