# Target Errors and Port Defaults Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. This checkout has no `HEAD`, so a Git worktree or safe task-only commit is not possible; preserve all existing untracked files and do not commit/push without a separate baseline decision.

**Goal:** Return AI infrastructure target-parse issues through the Agent/SSE error event and show them line by line, while making `full_tcp` the default for CLI and legacy WebSocket scans.

**Architecture:** Keep the task/SSE envelope unchanged. Wrap parse errors in Agent with their original `targets.LineError` values, map them to an optional JSON `targetIssues` field at the event boundary, and upsert the existing front-end error message so late-arriving details are not lost. Normalize port mode at the two user-facing legacy ingress points; leave Runner's internal empty mode available to avoid an Agent double-scan.

**Tech Stack:** Go 1.23.2, React/TypeScript/Vite, Node 24 built-in test runner, Docker-hosted Go toolchain, existing i18n JSON.

**Approved spec:** `docs/superpowers/specs/2026-09-29-target-errors-port-default-design.md`

---

## Preconditions and file map

- Repository: `D:\June\E\code\zcode\AIG改造\AI-Infra-Guard` (all project files currently untracked; no `HEAD`). Do not reset, overwrite unrelated files, or commit/push.
- Baseline Go toolchain: `golang:1.23.2-bookworm` is cached locally. For repeated red/green cycles, run one temporary container:

```powershell
docker run -d --rm --name aig-go-test-0929 --mount "type=bind,source=D:\June\E\code\zcode\AIG改造\AI-Infra-Guard,target=/src,readonly" -w /src golang:1.23.2-bookworm sleep infinity
```

  Then run `docker exec aig-go-test-0929 go test <packages>`; stop the exact container with `docker stop aig-go-test-0929` at the end. If already occupied, inspect it rather than replacing it.
- `common/agent/target_parse_error.go` (new): typed parse error and conversion to transport-only `TargetIssue`.
- `common/targets/parser.go`, `parser_test.go`: preserve the original invalid line in `LineError.Input`.
- `common/agent/types.go`, `agent.go`, `tasks.go`: optional error-event field, send path, parse-failure wrapping.
- `common/agent/target_parse_error_test.go` (new): invalid-line, global-line, generic-error, JSON-contract tests.
- `cmd/cli/cmd/scan.go`, `scan_port_mode_test.go` (new): default normalization and CLI flag/help contract.
- `common/websocket/websocket.go`, `websocket_port_mode_test.go` (new): request normalization at the old WebSocket scan entry.
- `frontend/src/utils/targetIssues.ts`, `targetIssues.test.mjs` (new): defensive event parsing and duplicate-message upsert.
- `frontend/src/components/TargetIssueList.tsx` (new), `ChatArea.tsx`, `frontend/src/types/index.ts`, `frontend/src/context/AppContext.tsx`, `frontend/src/i18n/locales/{zh,en}.json`: small error detail UI and state.
- `api.md`, `api_zh.md`, `docs/swagger.yaml`, CLI help text: request/response and changed-default documentation.
- Existing baseline limitations: `common/agent/tasks_test.go` does not compile (references removed Agent test symbols). Its focused new tests must use an explicit Go-file list that excludes that unrelated stale test. Existing `common/websocket/api_test.go` has five English-vs-Chinese expectation failures; report separately from changed tests.

## Task 1: Agent parse errors reach the existing SSE event

**Files:** `common/targets/parser_test.go`, `common/targets/parser.go`, `common/agent/target_parse_error_test.go`, `common/agent/target_parse_error.go`, `common/agent/types.go`, `common/agent/agent.go`, `common/agent/tasks.go`

- [x] **Step 1: Write failing Go tests.** First assert `targets.Parse` retains the original invalid text in `Issues[0].Input`; run `go test ./common/targets` for RED. From two invalid input lines, call `scanPortsAndPrepareTargets` and assert `errors.As(err, &targetParseError)` with line 1 and line 2 in order. Construct an Agent with a buffered `sendChan`, call a planned `SendTaskError(sessionID, err)` helper, receive the channel item, marshal it, and assert `event.message` plus `event.targetIssues` with lowercase `line/input/reason/suggest`. Test `line: 0` mapping and a generic error without the optional field.
- [x] **Step 2: Verify RED.** Agent package baseline is unbuildable because stale `tasks_test.go` references removed symbols. Use explicit non-test Go files plus the new test file: `docker exec aig-go-test-0929 sh -c 'go test $(find common/agent -maxdepth 1 -name "*.go" ! -name "*_test.go") common/agent/target_parse_error_test.go'`. Failure was specifically the absent typed error and send helper. (`sh -lc` removed Go from this image's PATH; use `sh -c`.)
- [x] **Step 3: Implement the minimal path.** Add a typed wrapper that retains the parser's summary `Error()` and original issues, plus transport `TargetIssue` with explicit JSON tags. Wrap `targets.Parse` failures in `scanPortsAndPrepareTargets`. Add optional `ErrorEvent.TargetIssues` and `SendTaskError` (generic errors continue through existing `SendError`). Replace the task-execution goroutine's direct `SendError(err.Error())` call with the helper; leave cancellation behavior unchanged. Avoid changing task manager/SSE outer format.
- [x] **Step 4: Verify GREEN and package baseline.** Focused Agent test passed; `common/targets` parser test passed. Full Agent package remains blocked by the pre-existing stale test.

## Task 2: Normalize user-facing port defaults, never the Agent's internal Runner pass

**Files:** `cmd/cli/cmd/scan_port_mode_test.go`, `cmd/cli/cmd/scan.go`, `common/websocket/websocket_port_mode_test.go`, `common/websocket/websocket.go`, `common/portscan/mode.go`, `internal/options/options.go`

- [x] **Step 1: Write failing Go tests.** Assert CLI `--port-mode` flag default is `full_tcp`; a small CLI normalization function maps empty/full/fixed correctly and rejects an invalid value. Assert a WebSocket request-normalization function maps missing/empty to `full_tcp`, keeps `fixed_ai` with exactly `11434,1337,7000-9000,18789`, and rejects an invalid value. Add a `portscan` table test if the existing package lacks coverage for empty/default, fixed and invalid values. These tests must not invoke nmap.
- [x] **Step 2: Verify RED.** CLI and WebSocket tests failed because the ingress normalization helpers were absent; `portscan` existing contract test passed.
- [x] **Step 3: Implement the minimal ingress changes.** CLI flag/help uses `full_tcp` default and normalizes explicit empty before populating `Options.PortMode`; invalid values exit nonzero via the current CLI error path. Old WebSocket scan handler normalizes `req.PortMode` to `full_tcp` before `runner.New`, returning an existing error log for invalid values. Keep `runner.Options.PortMode == ""` as an internal skip signal so Agent's second Runner pass does not nmap again; update stale comments to explain this boundary.
- [x] **Step 4: Verify GREEN.** Focused CLI, WebSocket and portscan tests passed. Agent's internal Runner options still leave `PortMode` unset. Five pre-existing full WebSocket assertions are tracked separately.

## Task 3: Defensively parse and upsert front-end error details

**Files:** `frontend/src/utils/targetIssues.test.mjs`, `frontend/src/utils/targetIssues.ts`, `frontend/src/types/index.ts`, `frontend/src/context/AppContext.tsx`, `frontend/src/components/ChatArea.tsx`

- [x] **Step 1: Write failing Node tests.** Defensive parsing, both event orders, queue admission and persisted-event folding are covered.
- [x] **Step 2: Verify RED.** Utility module was absent; Node test failed with `ERR_MODULE_NOT_FOUND`.
- [x] **Step 3: Implement the minimal utility and state path.** Shared parser/upsert helper is used by SSE, queue admission, reducer and task reload.
- [x] **Step 4: Verify GREEN.** Six Node utility/render tests passed and `pnpm exec tsc -b` passed.

## Task 4: Render per-line issues in the existing error card

**Files:** `frontend/src/components/TargetIssueList.tsx` (new), `frontend/src/components/ChatArea.tsx`, `frontend/src/i18n/locales/zh.json`, `frontend/src/i18n/locales/en.json`, `frontend/src/components/TargetIssueList.test.mjs` (new)

- [x] **Step 1: Write a failing render test.** Vite SSR test asserts ordered rows, global label, suggestion and escaped input.
- [x] **Step 2: Verify RED.** Test failed solely because the new component was absent.
- [x] **Step 3: Implement the component and integrate it.** Existing error card retains summary and now renders a compact, scrollable semantic list with i18n labels.
- [x] **Step 4: Verify GREEN and visual check.** Six Node tests, `tsc -b`, and Vite open-source build passed. A temporary localhost fixture using the production `TargetIssueList` was inspected at desktop and 390 px width, then removed. This is visual QA only, not real Agent/SSE integration.

## Task 5: Document behavior and run final checks

**Files:** `api.md`, `api_zh.md`, `docs/swagger.yaml`, `docs/gaizao/input-formats-phase1-plan.md` only if a status note is useful; `cmd/cli/cmd/scan.go` help text already in Task 2.

- [x] **Step 1: Update docs.** Updated both API languages and Swagger with mode choices, SSE details, scope, and CLI/legacy WebSocket compatibility note.
- [x] **Step 2: Validate docs and all changed paths.** Swagger YAML and both i18n JSON files parse. Focused Go and six Node tests, TS build, Vite build and targeted ESLint pass. Full Go suite fails on pre-existing stale Agent tests, five WebSocket language assertions and unrelated tests requiring local paths/browser/HTTP fixture; these are recorded separately.
- [x] **Step 3: Controlled integration attempt.** No current-code server/Agent runs on localhost. An older Docker preview stack exists but uses a prebuilt image without source bind mounts; rebuilding/replacing it could disrupt unrelated user state. Therefore no real Agent/SSE scan was run or claimed. The localhost component fixture was removed after visual QA.
- [x] **Step 4: Review and handoff.** Independent code review completed with SSE lifecycle fixes. Focused Go and 14 front-end tests, TypeScript, Vite, new-file ESLint and docs parsing passed. `ChatArea.tsx` retains pre-existing lint failures; full Go suite retains pre-existing/environment failures. Dependency cache/build output are ignored. The temporary Go container is stopped at handoff. No commit, push or PR because this Git repository has no established baseline and the user has not requested publication.
