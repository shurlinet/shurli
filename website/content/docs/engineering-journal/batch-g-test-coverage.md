---
title: "Batch G - Test Coverage"
weight: 9
description: "Coverage-instrumented Docker tests, relay binary, injectable exit, post-phase audit protocol."
---
<!-- Auto-synced from docs/engineering-journal/batch-g-test-coverage.md by sync-docs - do not edit directly -->


Coverage-instrumented Docker tests, relay binary, injectable exit, and post-phase audit protocol.

---

### ADR-G01: Coverage-Instrumented Docker Tests

**Context**: Docker integration tests verify real binaries in containers but didn't contribute to coverage metrics. Needed to merge Docker test coverage with unit test coverage.

**Alternatives considered**:
- **Separate coverage reports** - Track Docker and unit coverage independently. Rejected because it gives an incomplete picture.
- **Coverage at the Go test level only** - Skip Docker coverage. Rejected because the Docker tests exercise critical paths (relay, invite/join) that unit tests can't.

**Decision**: Build binaries with `-cover -covermode=atomic`, set `GOCOVERDIR` in containers, extract coverage data after tests, merge with unit test profiles using `go tool covdata`. Combined coverage reported in CI.

**Consequences**: Docker tests are slower (coverage instrumentation adds overhead), but we get accurate end-to-end coverage numbers. The merged profile reveals which code paths are only exercised by integration tests.

**Reference**: `test/docker/integration_test.go`, `.github/workflows/ci.yml`

---

### ADR-G02: Relay-Server Binary in Integration Tests

**Context**: Docker integration tests need to run the relay server. The relay server is built from `cmd/relay-server/`.

**Alternatives considered**:
- **Use a public relay** - Test against a real relay. Rejected because tests must be self-contained and reproducible.
- **Mock relay in-process** - Use libp2p relay transport directly. Rejected because we want to test the actual relay-server binary.

**Decision**: Build `relay-server` binary alongside `shurli` binary for Docker tests. The compose file starts a relay container, and node containers use it for circuit relay.

**Consequences**: Tests verify the actual deployment path (binary -> container -> relay -> circuit). Takes longer to build but catches real integration issues.

**Reference**: `test/docker/compose.yaml`, `test/docker/Dockerfile`

---

### ADR-G03: Injectable `osExit` for Testability

**Context**: Several commands call `os.Exit()` on error. This kills the test process, making those code paths untestable.

**Alternatives considered**:
- **Panic + recover** - Use `panic` instead of `os.Exit` and recover in tests. Rejected because panics have different semantics (stack traces, deferred functions).
- **Return error codes** - Refactor all commands to return errors. Considered for future, but too large a refactor for a testing improvement.

**Decision**: Package-level `var osExit = os.Exit` that tests override with a function that records the exit code instead of terminating. Applied to `https://github.com/shurlinet/shurli/blob/main/cmd/shurli/` (the main binary) and `cmd/relay-server/`.

**Consequences**: Minimal code change (one variable + one test helper), enables testing of all exit paths. The variable is package-level, so tests must be careful about parallel execution (each test restores the original `osExit`).

**Reference**: `https://github.com/shurlinet/shurli/blob/main/cmd/shurli/run.go`, `https://github.com/shurlinet/shurli/blob/main/cmd/shurli/run_test.go`

---

### ADR-G04: Post-Phase Audit Protocol

**Context**: After completing each batch, need a systematic review to catch issues before moving to the next phase. Ad-hoc reviews miss things.

**Alternatives considered**:
- **Ad-hoc review** - Review when something feels wrong. Rejected because it's inconsistent and misses systematic issues.
- **External audit** - Hire security auditors. Planned for later stages, but too expensive for every batch.

**Decision**: Mandatory 6-category audit after every phase: source code audit, bad code scan, bug hunting, QA testing, security audit, and relay hardening review. Each category has specific checklists. Findings are compiled into a report, and fixes require explicit approval before implementation.

The Batch G audit found 10 issues (CVE in pion/dtls, TOCTOU on Unix socket, cookie ordering, body size limits, CI SHA pinning, etc.) - all fixed in commit `83d02d3`.

**Consequences**: Adds time between batches, but catches real issues. The audit that found the pion/dtls nonce-reuse CVE justified the entire protocol - that vulnerability could have compromised encrypted relay traffic.

**Reference**: Audit findings tracked in project memory, fixes in commit `83d02d3`
