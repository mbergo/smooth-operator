# Smooth Operator v2 — Task Breakdown

Derived from `docs/PRD-v2.md`. Tasks are grouped by phase. Each task lists:

- **Owner asset**: the agent / skill / persona best suited (per the global asset-eval rule).
- **Parallelizable**: whether it can run in a fan-out batch alongside others.
- **Deps**: prerequisite task IDs.
- **DoD**: definition of done.

Phases line up with PRD Section 14. Parallel agents are explicitly grouped so the orchestrator can dispatch them concurrently.

---

## Phase 0 — Skeleton (1 week)

### T0.1 Spike: ArgoCD MCP availability

- **Owner asset**: `mcp-developer` agent + Context7 fetch for ArgoCD ecosystem.
- **Parallel**: yes (with T0.2, T0.3).
- **Deps**: none.
- **DoD**: written one-pager `docs/spikes/argocd-mcp.md` answering: does a maintained ArgoCD MCP exist? If yes, which methods exist? If no, sketch a raw-HTTP wrapper API matching the operator's needs (`upsert`, `sync`, `watch`, `getStatus`). Decision noted.

### T0.2 Spike: jules.google.com MCP availability

- **Owner asset**: `mcp-developer` agent + WebFetch.
- **Parallel**: yes.
- **Deps**: none.
- **DoD**: `docs/spikes/jules-mcp.md`. Decision: integrate, skip, or defer.

### T0.3 Spike: Electrobun vs Tauri vs Wails

- **Owner asset**: `frontend-developer` + `Startup CTO` agent for trade-off framing.
- **Parallel**: yes.
- **Deps**: none.
- **DoD**: `docs/spikes/ui-framework.md`. Sample "hello ASCII" app shipped in each candidate. Pick one. Default tilt: Electrobun (user-requested), Wails as backup (Go-native).

### T0.4 Repo scaffold

- **Owner asset**: `golang-pro` agent.
- **Parallel**: no (blocks Phase 1 work).
- **Deps**: none.
- **DoD**:
  - New `cmd/smoothop/main.go` entrypoint. No Kubernetes imports.
  - Top-level dirs: `internal/journal/`, `internal/prompts/`, `internal/redis/`, `internal/bus/`, `internal/state/`.
  - `go.mod` cleaned of controller-runtime / k8s.io dependencies that v2 doesn't use.
  - Existing `internal/policy/`, `internal/planner/validator.go`, `internal/llm/`, `internal/gitops/` preserved (these get refactored later, not deleted).

### T0.5 Redis bootstrap

- **Owner asset**: `golang-pro` + `database-administrator`.
- **Parallel**: yes (with T0.6).
- **Deps**: T0.4.
- **DoD**:
  - `internal/redis/` wraps `github.com/redis/go-redis/v9`.
  - Operator boot detects existing local Redis on `127.0.0.1:6379`; if absent, starts a Docker container `bitnami/redis:7-debian-12` with AOF enabled.
  - Connection pool sized for one project at a time.
  - Unit tests against `miniredis` (in-memory stub).

### T0.6 Journal primitive

- **Owner asset**: `golang-pro`.
- **Parallel**: yes.
- **Deps**: T0.5.
- **DoD**:
  - `internal/journal/` exposes `Intent(op, ref, payload)`, `Start(id)`, `Success(id, result)`, `Fail(id, err)`, `OpenIntents(projectId) []Entry`.
  - Backed by Redis Streams (`project:<id>:journal`).
  - Payloads stored separately under `:payload:<sha>` and referenced by hash.
  - Idempotent replay on startup: `OpenIntents` returns intents without matching success/fail.
  - Unit tests against `miniredis`.

### T0.7 Static prompt catalog scaffolding

- **Owner asset**: `prompt-engineer` agent.
- **Parallel**: yes.
- **Deps**: T0.4.
- **DoD**:
  - `internal/prompts/` with one file per template: `initial_proposal.go`, `materialize.go`, `fix_loop.go`, `resume_context.go`, `gemini_mirror.go`.
  - Each file exposes `System() string`, `User(body any) (string, error)` returning the rendered prompt.
  - All Go `text/template` rendering — no string concatenation in calling code.
  - Schemas (Go structs) for `ProposalBundle`, `ResourceBundle`, `ErrorReport`, `RepoInvestigationReport`. JSON tags + `validate` tags.
  - Table-driven tests: render each template with sample bodies, assert structural invariants (system prefix unchanged, body interpolated, no extra whitespace drift that would break prompt cache).

---

## Phase 1 — Anthropic chain (1 week)

### T1.1 Stateless Anthropic RPC client

- **Owner asset**: `golang-pro` + `claude-api` skill.
- **Parallel**: no (blocks rest of Phase 1).
- **Deps**: T0.7.
- **DoD**:
  - `internal/llm/anthropic/client.go` — pure RPC. Single method `Call(ctx, system, conversation []Message) (Message, Usage, error)`.
  - Adaptive thinking, xhigh effort, cache_control on system block.
  - Retry on 429 / 5xx; respects `Retry-After`.
  - No Kubernetes types in scope. No CRD watch. Pure stdlib + SDK.
  - Streaming variant `CallStream(ctx, ...) <-chan Event` for the UI chat screen.

### T1.2 Conversation thread persistence

- **Owner asset**: `golang-pro`.
- **Parallel**: yes (with T1.3).
- **Deps**: T1.1.
- **DoD**:
  - `internal/state/conversation.go` maintains the ordered message list in Redis (`project:<id>:conversation`).
  - Hashes payloads to `:payload:<sha>`; the conversation list stores SHAs only.
  - `Replay(ctx) []Message` rebuilds the thread for the next API call.
  - Beta header `compact-2026-01-12` wired through when conversation length crosses threshold.

### T1.3 ResourceBundle / ProposalBundle decoder

- **Owner asset**: `golang-pro` + `prompt-engineer` for schema validation rules.
- **Parallel**: yes.
- **Deps**: T0.7.
- **DoD**:
  - `internal/state/bundles.go` exposes typed structs + `DecodeProposalBundle`, `DecodeResourceBundle`.
  - Tolerates ```` ```json ```` fences (Opus quirk).
  - Validates Kind allowlist, namespace consistency, helm chart structural sanity.
  - Reuses `internal/policy/` allowlist + `internal/planner/validator.go` cluster-scoped guard.
  - Returns structured errors with field paths.

### T1.4 End-to-end Initial→Materialize against mocked repo

- **Owner asset**: `test-automator` + `golang-pro`.
- **Parallel**: no.
- **Deps**: T1.1, T1.2, T1.3.
- **DoD**:
  - Integration test: httptest stub of Anthropic Messages API + a faked `RepoInvestigationReport`. Drives the chain: `INITIAL_PROPOSAL` → user-accepts → `MATERIALIZE`. Asserts a valid `ResourceBundle` lands in `:last_bundle`.
  - Cache hit assertion: second call within the same project sees `cache_read_input_tokens > 0`.

---

## Phase 2 — GitHub + Helm (1 week)

### T2.1 Local-mode git client

- **Owner asset**: `golang-pro`.
- **Parallel**: yes (with T2.2).
- **Deps**: T0.4.
- **DoD**:
  - `internal/gitops/local.go` — clone, checkout, branch, commit (signed, no AI refs), push.
  - Uses OS keychain for credential lookup (`go-keyring`).
  - Author = `smooth-operator[bot]`; co-author trailer = user's local git config.
  - All ops idempotent against a `{projectId, op, ref}` key — safe to replay from journal.

### T2.2 Helm chart packager

- **Owner asset**: `golang-pro`.
- **Parallel**: yes.
- **Deps**: T0.4.
- **DoD**:
  - `internal/gitops/helm.go` (existing file from v1 — refactor for stateless use).
  - `Package(chart *HelmChart) (tgz []byte, version string, err error)`.
  - Semver bump policy: y on fix-loop, x on user-driven edit, z reserved.
  - Helm lint runs inline via `helm.sh/helm/v3` Go API; lint failure = hard error before publish.

### T2.3 GitHub Packages OCI publisher

- **Owner asset**: `golang-pro` + `deployment-engineer` for OCI conventions.
- **Parallel**: yes.
- **Deps**: T2.2.
- **DoD**:
  - `internal/gitops/publish.go` — `helm push oci://ghcr.io/<owner>/<repo>/charts/<name>:<version>`.
  - Uses the same PAT auth as commits.
  - Returns the resolved OCI URL for inclusion in the ArgoCD `Application`.
  - Idempotent retry on `409 already exists`: skip and return existing URL.

### T2.4 Bundle applier (Role 2 executor)

- **Owner asset**: `golang-pro`.
- **Parallel**: no (integrates T2.1–T2.3).
- **Deps**: T2.1, T2.2, T2.3, T0.6.
- **DoD**:
  - `internal/exec/apply.go` — `Apply(ctx, bundle *ResourceBundle) error`.
  - Step order: write files → git commit → helm package → helm push → return OCI URL.
  - Each step journaled (intent / start / success / fail).
  - Crash mid-step resumes from `OpenIntents` on next boot.

---

## Phase 3 — ArgoCD MCP (1 week)

### T3.1 ArgoCD client adapter

- **Owner asset**: `golang-pro` + `kubernetes-specialist` for ArgoCD CRD knowledge.
- **Parallel**: no (blocks rest of Phase 3).
- **Deps**: T0.1 (spike decided MCP vs raw HTTP).
- **DoD**:
  - `internal/argo/client.go` with one method per operator need: `Upsert(ctx, app)`, `Sync(ctx, appName)`, `GetStatus(ctx, appName) AppStatus`, `Watch(ctx, appName) <-chan AppStatus`.
  - If MCP available: thin wrapper over MCP calls.
  - If MCP absent: raw HTTPS calls to ArgoCD API + bearer token.
  - One config knob `argocd.mode = "mcp" | "http"` decides at boot.

### T3.2 Application status interpreter

- **Owner asset**: `golang-pro`.
- **Parallel**: yes (with T3.3).
- **Deps**: T3.1.
- **DoD**:
  - `internal/argo/interp.go` maps ArgoCD `Application.status.conditions` + `health.status` + `sync.status` into one of three outcomes: `OK`, `RetryableFail`, `EscalateToOpus`.
  - Pod event + recent log fetch on `EscalateToOpus`: bundles them into an `ErrorReport`.

### T3.3 Fix-loop dispatcher

- **Owner asset**: `golang-pro` + `error-coordinator`.
- **Parallel**: yes.
- **Deps**: T3.1.
- **DoD**:
  - `internal/loop/fix.go` — on `EscalateToOpus`, package `ErrorReport`, send `FIX_LOOP` prompt to Anthropic, decode `ResourceBundle`, call `Apply`, watch status, repeat.
  - Bounded retries (default 5).
  - On exhaustion: surface to UI with full context.

### T3.4 Integration test against local ArgoCD

- **Owner asset**: `test-automator` + `kubernetes-specialist`.
- **Parallel**: no.
- **Deps**: T2.4, T3.1, T3.2, T3.3.
- **DoD**:
  - CI workflow `argocd-e2e.yml` spins up `kind` + ArgoCD via helm, runs a deliberately-broken bundle, asserts the fix loop converges within 3 rounds.

---

## Phase 4 — Electrobun UI (2 weeks)

### T4.1 UI app scaffold

- **Owner asset**: `frontend-developer` agent (or `mobile-developer` if Electrobun ergonomics overlap).
- **Parallel**: no (blocks rest of Phase 4).
- **Deps**: T0.3 spike decided framework.
- **DoD**:
  - Electrobun project under `ui/` (separate go.mod or workspace member).
  - TypeScript or Go binding per framework choice.
  - Builds a binary that opens a native window with "Hello ASCII" inside a monospace `<pre>` element.

### T4.2 WebSocket protocol with operator

- **Owner asset**: `websocket-engineer`.
- **Parallel**: yes (with T4.3).
- **Deps**: T4.1.
- **DoD**:
  - Operator exposes `/ws` on `127.0.0.1:<ephemeral>` printed to stdout at boot.
  - JSON envelope schema documented + typed on both ends.
  - One-time bootstrap token in URL query param; rejected on reuse.
  - Reconnect / heartbeat semantics defined.

### T4.3 ASCII component library

- **Owner asset**: `ui-designer` + `frontend-developer`.
- **Parallel**: yes.
- **Deps**: T4.1.
- **DoD**:
  - Reusable components: `Tree`, `Table`, `Box`, `Spinner`, `Progress`, `MessageStream`, `KeyHints`.
  - All monospace; no images; no SVG.
  - Storybook-equivalent (a `/dev` route in the UI app) renders each component with sample data.

### T4.4 Eight screens

- **Owner asset**: `frontend-developer` × 4 parallel (two screens per agent).
- **Parallel**: yes.
- **Deps**: T4.3.
- **DoD**: Init, Investigating, Proposal, Chat, Materialize, Watching, Fix-Loop, Complete — each implemented + screenshot-tested (golden ASCII snapshot in repo).

### T4.5 UI ↔ Operator command mapping

- **Owner asset**: `golang-pro` (operator side) + `frontend-developer` (UI side).
- **Parallel**: no.
- **Deps**: T4.2, T4.4.
- **DoD**:
  - All eight client commands (`init`, `accept_proposal`, ...) wired through the WS protocol.
  - Server-pushed events delivered correctly to the right screen.

---

## Phase 5 — Gemini fallback + diff mode (1 week)

### T5.1 Gemini Pro 3.1 client

- **Owner asset**: `golang-pro` + `ai-engineer`.
- **Parallel**: yes (with T5.2).
- **Deps**: T1.1.
- **DoD**:
  - `internal/llm/gemini/client.go` — mirror surface of the Anthropic client.
  - Same `Call(ctx, system, conversation)` signature.
  - Auth via `GEMINI_API_KEY` env (or keychain).
  - Mirror prompts in `internal/prompts/gemini_mirror.go`.

### T5.2 Fallback router

- **Owner asset**: `golang-pro`.
- **Parallel**: yes.
- **Deps**: T1.1, T5.1.
- **DoD**:
  - `internal/llm/router.go` — picks Anthropic by default; falls back to Gemini if Anthropic fails ≥ 2 times in 60s or returns `service_unavailable`.
  - Returns to Anthropic on first successful health probe.
  - Operator-side stitching: maintains a parallel Gemini conversation thread so fallback doesn't lose context.

### T5.3 Diff mode

- **Owner asset**: `golang-pro` + `caveman:cavecrew-builder` (for surgical patch application).
- **Parallel**: yes.
- **Deps**: T1.3, T2.4.
- **DoD**:
  - `--diff-mode` CLI flag.
  - When enabled, `FIX_LOOP` prompt requests diff output.
  - Diff applier handles `create | update | delete` operations against the local repo before commit.
  - Validation runs after diff apply (same Kind allowlist + namespace guard).
  - Fall back to full-bundle mode on diff parse failure.

---

## Phase 6 — Hardening + docs (1 week)

### T6.1 Threat model review

- **Owner asset**: `security-auditor` + `security-engineer`.
- **Parallel**: yes (with T6.2 and T6.3).
- **Deps**: T2.4, T3.4, T5.2.
- **DoD**:
  - `docs/threat-model-v2.md` covering: local credential exposure, Redis exposure, MCP server trust boundaries, prompt injection via Repo contents, Gemini fallback divergence, journal tampering.
  - BLOCKER findings closed in code; HIGHs documented + scheduled.

### T6.2 E2E test against real kind + ArgoCD

- **Owner asset**: `test-automator` + `qa-expert`.
- **Parallel**: yes.
- **Deps**: T3.4, T4.5.
- **DoD**:
  - GitHub Actions workflow that spins kind, installs ArgoCD, drives `smoothop` end-to-end with a fixture repo, asserts `Synced+Healthy`.
  - Negative test: corrupted bundle on first apply → fix loop converges.

### T6.3 User docs

- **Owner asset**: `technical-writer` + `documentation-engineer`.
- **Parallel**: yes.
- **Deps**: T4.5.
- **DoD**:
  - `docs/quickstart-v2.md` — 5-minute happy path.
  - `docs/configuration.md` — flags + env vars + secrets handling.
  - `docs/troubleshooting.md` — common failure modes + journal-based recovery walkthrough.

### T6.4 v1 deprecation note

- **Owner asset**: `technical-writer`.
- **Parallel**: yes.
- **Deps**: none.
- **DoD**:
  - `docs/MIGRATION-v1-to-v2.md` — explains how the in-cluster operator from PRs #2–#5 maps onto v2; lists deprecated CRDs (`ChatSession`, `SmoothAction`); recommends a graceful sunset path for existing v1 deployments.

---

## Cross-cutting tasks

### X.1 CI for v2 binary

- **Owner asset**: `deployment-engineer`.
- **Parallel**: yes.
- **Deps**: T0.4.
- **DoD**: Adapt existing `.github/workflows/build.yml` to build `cmd/smoothop`. Coverage gate kept ≥ existing threshold.

### X.2 Release tooling

- **Owner asset**: `deployment-engineer` + `release-manager` skill.
- **Parallel**: yes.
- **Deps**: T6.3.
- **DoD**: `goreleaser` config builds darwin / linux / windows binaries + an Electrobun bundle. Signed releases.

### X.3 Memory / context update

- **Owner asset**: self (memory system).
- **Parallel**: yes.
- **Deps**: PRD approval.
- **DoD**: New memory entry under `feedback_*` capturing v2 architectural decisions so future sessions don't re-derive them.

---

## Parallel fan-out plan

Phase 0: T0.1, T0.2, T0.3, T0.5, T0.6, T0.7 can all dispatch in parallel after T0.4 lands. That's 6 agents in one batch.

Phase 1: T1.2 + T1.3 parallel after T1.1.

Phase 2: T2.1 + T2.2 + T2.3 parallel; T2.4 integrates.

Phase 3: T3.2 + T3.3 parallel after T3.1.

Phase 4: T4.4 splits into 4 parallel screen pairs.

Phase 5: T5.1 + T5.2 + T5.3 all parallel.

Phase 6: T6.1 + T6.2 + T6.3 + T6.4 all parallel.

Cross-cutting: X.1 + X.2 + X.3 parallel anywhere they fit.

Total parallel-agent slots across the build: ~28.

---

## Acceptance Criteria for v2

- A fresh user clones a fresh repo with only a Dockerfile, runs `smoothop init <repo>`, completes the Init screen, accepts the first proposal, and within 15 minutes the ArgoCD UI shows the app as `Synced + Healthy`.
- Killing the operator at any point during the flow and restarting it resumes from the journal without re-prompting the user.
- A deliberately broken chart (bad image tag) triggers the fix loop and converges within 3 rounds.
- Anthropic API outage triggers Gemini fallback transparently; user sees only a small badge in the UI.
- Final repo state: `.smoothop/` directory with a complete Helm chart, ArgoCD `Application`, signed commits, helm package in GitHub Packages, no operator-emitted credentials in any file.
