# Smooth Operator — PRD v2

**One-liner:** A local-first deployment companion. User points it at a GitHub repo; the operator investigates, drafts a deployment plan, hands the conversation to Claude Opus 4.7 inside a native TUI dashboard until consensus, then commits the resulting artifacts (Helm chart, Terraform, ArgoCD `Application`) to a folder it owns in the repo, publishes the chart to the repo's package registry, registers the deployment with ArgoCD, and watches the rollout — routing any failure back through Opus until the deployment stabilizes. After first deploy, the operator bows out; ArgoCD takes over the steady-state reconciliation.

---

## 0. Document Control

- **Owner:** Platform
- **Version:** v2.0 (draft)
- **Status:** For review
- **Supersedes:** `PRJ.md` (v1 — in-cluster operator with embedded AI)

---

## 1. Problem & Goals

### 1.1 Problem

v1 placed AI inside a Kubernetes operator. That coupling forced cluster-wide RBAC, made the operator the security perimeter for prompt injection, and complicated multi-tenant boundaries. Users still need the same outcome — "I have a repo, get it deployed safely" — but the friction belongs outside the cluster, not inside it.

### 1.2 Goals

- **Local-first**: the operator runs on the user's machine for first deploy and tears down after handoff.
- **AI lives outside Kubernetes**: Claude Opus 4.7 owns the conversation. The operator is a dumb router; no LLM logic in cluster code.
- **GitHub is the source of truth**: every generated resource is committed before deploy. Helm packages publish to the same repo.
- **ArgoCD owns steady state**: the operator hands off to ArgoCD via MCP. After handoff, future image deploys flow through normal GitOps CI/CD.
- **Resumable**: a journal records intent → action → ack so any crash mid-flow restarts cleanly without re-prompting the user.
- **One AI per project**: the same Anthropic conversation thread persists for the entire bootstrap → fix-loop ordeal. Gemini Pro 3.1 only as a hard fallback.

### 1.3 Non-Goals

- No replacement for ArgoCD's reconciliation loop. We register apps; ArgoCD reconciles.
- No multi-tenant SaaS. v2 is single-user, local-first.
- No automated Day-2 advice (autoscaling tuning, cost optimization). v2 ends after first stable deploy.
- No webhook listener for repo changes. CI/CD handles that downstream.

### 1.4 Success Metrics

- **Time to first stable deploy**: P50 < 15 min from `smoothctl init <repo>` to ArgoCD `Synced`.
- **User intervention beyond the consensus dialogue**: 0 manual `kubectl` / `helm` / `argocd` commands required during the first-deploy ordeal.
- **Crash-recovery success rate**: ≥ 99% of mid-flow crashes resume from the journal without bothering the user.
- **Fix-loop convergence**: ≥ 80% of failed deploys reach `Synced+Healthy` within 3 Opus fix rounds.

---

## 2. Personas & Use Cases

### 2.1 Personas

- **App dev** (primary): has a repo with a Dockerfile and a vague idea of "this needs to run somewhere". Knows Git, knows Docker, does not know Helm/ArgoCD/RBAC.
- **Platform engineer** (secondary): runs ArgoCD and wants developers to self-serve onboarding without filing tickets.

### 2.2 Use Cases

1. **Greenfield deploy**: dev has a fresh repo with a Dockerfile; smooth-operator drafts a complete Helm + ArgoCD `Application`, commits, deploys.
2. **Existing chart adoption**: dev has a partial `chart/` directory; operator inspects, extends, commits the gap, deploys.
3. **Fix loop**: ArgoCD reports `OutOfSync` after deploy; operator routes the error to Opus; Opus emits a corrected manifest set; operator re-commits + re-publishes + retriggers Argo until `Synced+Healthy`.
4. **Resume after crash**: user's laptop sleeps mid-deploy; on wake, operator reads its journal, re-establishes context with Opus, continues exactly where it left off.

---

## 3. Architecture Overview

### 3.1 ASCII

```
  ┌────────────────────────────────────────────────────────────────────┐
  │                       User's local machine                          │
  │                                                                     │
  │   ┌─────────────────┐         ┌───────────────────────┐             │
  │   │  Electrobun UI  │ ◄─────► │     Operator (Go)     │             │
  │   │  (native WV +   │  WSS    │                       │             │
  │   │  ASCII / TUI    │         │  Roles:               │             │
  │   │  components)    │         │   1. Watcher          │             │
  │   └─────────────────┘         │   2. K8s/Argo/GH      │             │
  │                               │      Helm-publisher   │             │
  │   ┌─────────────────┐         │   3. Context Router   │             │
  │   │  Redis (local)  │ ◄─────► │                       │             │
  │   │  - state        │         │  Journal: Redis       │             │
  │   │  - journal      │         │           Streams     │             │
  │   │  - hot context  │         └──────┬────────────────┘             │
  │   └─────────────────┘                │                              │
  │                                      │                              │
  └──────────────────────────────────────┼──────────────────────────────┘
                                         │
              ┌──────────────────────────┼────────────────────────────┐
              │                          │                            │
              ▼                          ▼                            ▼
       ┌──────────────┐         ┌──────────────────┐         ┌──────────────┐
       │  Anthropic   │         │  GitHub API +    │         │  ArgoCD MCP  │
       │  Opus 4.7    │         │  Packages (helm) │         │   (server)   │
       │  (primary)   │         │                  │         │              │
       │              │         │  - commits to    │         │  - register  │
       │  ─────       │         │    .smoothop/    │         │    Apps      │
       │  Gemini 3.1  │         │  - chart pkgs    │         │  - read sync │
       │  (fallback)  │         │                  │         │    status    │
       └──────────────┘         └──────────────────┘         └──────┬───────┘
                                                                    │
                                                                    │  GitOps reconcile
                                                                    ▼
                                                            ┌──────────────┐
                                                            │ Target K8s   │
                                                            │ Cluster      │
                                                            └──────────────┘
```

### 3.2 Components

| Component | Runtime | Responsibility |
|-----------|---------|----------------|
| **Electrobun UI** | Native webview on user's OS | TUI dashboard, ASCII illustrations, user input capture, real-time feed from operator over WebSocket |
| **Operator** | Single Go binary, local process | Watcher / Router / Publisher. Zero LLM logic. Static prompts only. |
| **Redis** | Local container or embedded | State store, journal (Redis Streams), hot prompt context cache |
| **Anthropic Messages API** | Cloud | Sole brain. Same conversation persists for entire project lifecycle. Adaptive thinking, xhigh effort, prompt caching on static role prompts. |
| **Gemini Pro 3.1** | Cloud | Hard fallback only — invoked if Anthropic is unreachable AND retries exhausted. Same prompt scaffolding. |
| **GitHub** | Cloud | Source of truth. Operator commits to a `.smoothop/` folder it owns. Helm packages publish to the repo's GitHub Packages. |
| **ArgoCD MCP** | Server (any reachable cluster) | Operator registers Argo `Application` CRs via MCP, polls sync status. After handoff, ArgoCD reconciles independently. |
| **jules.google.com MCP** *(optional)* | Cloud | If available, Jules performs the initial repo investigation. Otherwise the operator scans locally and Opus summarizes. |

### 3.3 Operator's Three Roles (no AI inside any of them)

The operator never reasons. It routes signals using a fixed catalog of static prompts. Each role sends a typed envelope to Opus and parses a typed response.

#### 3.3.1 Role 1 — Watcher

Watches GitHub commits to the project's `.smoothop/` folder, ArgoCD `Application.status`, and the Anthropic Messages stream. Emits typed events into a local event bus. Persists every observed event to the journal before passing it to the Router.

#### 3.3.2 Role 2 — K8s/Argo/GitHub/Helm Publisher

The "executor" arm. Receives a typed `ResourceBundle` from the Router, commits the contents to the repo's `.smoothop/` folder, packages the Helm chart, publishes to GitHub Packages, then calls ArgoCD MCP to register/update the `Application`. Each step writes a journal entry: `intent → start → success|fail`.

#### 3.3.3 Role 3 — Context Router

The conversational arm. Two channels:

- **User ↔ Opus**: relays user input from the Electrobun UI to Opus, streams Opus output back to the UI.
- **Opus ↔ Operator**: relays operator observations (deploy failures, ArgoCD `OutOfSync`, Watcher events) to Opus, parses Opus's `ResourceBundle` reply and hands it to Role 2.

Static prompts (Section 7) tell Opus which channel it is on and what signals to emit.

---

## 4. End-to-End Flow

### 4.1 First-deploy ordeal

```
1. User runs `smoothctl init https://github.com/owner/repo`
2. Operator boots, starts Redis, opens Electrobun UI window
3. Operator (or Jules MCP) clones + scans repo:
   - Dockerfile? entrypoint? exposed ports?
   - existing chart/, terraform/, k8s/?
   - language detection, framework hints
4. Operator emits Investigation Report to Opus via System+User message
   with the "Initial Proposal" static prompt (Section 7.1)
5. Opus returns a structured ProposalBundle (CRD-like JSON):
   { summary, deploymentMode, helm: {...}, terraform: {...}|null,
     argoCD: {...}, openQuestions: [...] }
6. UI renders ProposalBundle as ASCII dashboard:
   - tree view of files to be created
   - resource graph (Service -> Pod -> Image)
   - risk badges + confidence indicators
7. User reviews. Three options:
   (a) Accept -> jump to 10
   (b) Edit specific items -> 8
   (c) Free-form chat -> 9

8. UI sends EditRequest to Opus via Router
   (Opus stays in the same conversation thread; context cached)
9. Free-form chat: standard message exchange; ProposalBundle re-emitted
   each turn when state has changed
10. Consensus reached (user clicks "Apply" or Opus emits final-state token):
    Operator sends "Materialize" static prompt (Section 7.2) to Opus
    Opus returns the FINAL ResourceBundle:
      { commitFiles: [...],         # files + paths under .smoothop/
        helmChart: {...},           # chart files + values.yaml
        terraform: [...] | null,    # optional .tf files
        argoApplication: {...},     # ArgoCD Application CR
        notes: "..." }

11. Role 2 executes the ResourceBundle (each step journaled):
    a. git commit ResourceBundle.commitFiles to .smoothop/
    b. helm package -> .tgz
    c. publish .tgz to GitHub Packages (OCI registry on ghcr.io)
    d. ArgoCD MCP: upsert Application pointing at the published chart
    e. ArgoCD MCP: trigger sync
12. Role 1 (Watcher) polls ArgoCD Application.status
13. Two outcomes:
    (a) Synced + Healthy -> Operator emits FirstDeployComplete event,
        UI shows success, Operator process exits cleanly.
    (b) Failed / Degraded -> jump to Fix Loop
```

### 4.2 Fix loop (Watcher routes to Opus)

```
14. Watcher detects Argo Application = {Synced=false OR Healthy=false}
15. Watcher pulls error context: Argo conditions, pod events, recent logs
16. Router sends "Fix Loop" static prompt (Section 7.3) to Opus
    Body: ErrorReport JSON (conditions, events, log excerpts, current
    ResourceBundle digest)
17. Opus emits an updated ResourceBundle (diff or full, per
    section 7.3 mode)
18. Loop back to step 11.
19. Bounded retries (configurable; default 5). On exhaustion:
    - Surface to UI with full error context
    - User decides: continue, escalate to Gemini fallback, or abort
```

### 4.3 Crash recovery

```
On startup, operator checks journal head:
- If journal is empty -> fresh start, prompt for repo URL
- If journal has open intent without ack -> resume that intent:
   1. Re-read all journal entries for the project
   2. Reconstruct in-memory state (current ResourceBundle, last Opus
      message ID, ArgoCD app state)
   3. Re-establish Anthropic conversation context by replaying
      messages from journal up to last cached message ID
   4. Continue from the failed step (idempotent retry of Role 2 ops)
- User is never re-prompted for input that was already captured
```

### 4.4 Handoff to ArgoCD (operator exits)

```
On FirstDeployComplete:
- Operator writes a final journal entry: "Handoff"
- Operator commits a README.md to .smoothop/ explaining the
  ArgoCD Application and how to evolve it
- Operator process exits
- ArgoCD continues to reconcile the Application via normal GitOps
  (auto-sync on Git push, image updater if configured)
- Future image deploys = developer pushes new image tag -> CI/CD
  updates values.yaml in repo -> ArgoCD auto-syncs
```

---

## 5. Static Prompt Catalog

The operator never composes prompts dynamically. It owns a versioned catalog of static prompts in `internal/prompts/`. Each prompt is a frozen template; only the typed `{{ .Body }}` slot varies. Stable system prefixes mean Anthropic prompt caching hits across every reconcile.

### 5.1 Prompt: `INITIAL_PROPOSAL`

```
You are Smooth Operator, a deployment assistant. You are talking to a
USER who wants to deploy the repo described below to a Kubernetes
cluster managed by ArgoCD. The OPERATOR is a code-only router with no
intelligence; it relays your output to GitHub/Argo and relays user
input to you.

Output a single JSON ProposalBundle. Schema:
{
  "summary": "string, one paragraph",
  "deploymentMode": "helm | argocd-native | helm-via-argocd",
  "helm": { ... } | null,
  "terraform": { "files": [...] } | null,
  "argoApplication": { ... },
  "openQuestions": [ "string", ... ],
  "confidence": 0.0,
  "risk": "low|med|high"
}

Rules:
- Never invent secrets. Reference Secrets by name.
- Prefer Helm unless the project already uses Argo's native CRDs.
- Output ONLY the JSON. No markdown, no prose.

The repo investigation follows in the user message.
```

### 5.2 Prompt: `MATERIALIZE`

Sent after consensus. Asks Opus to produce the final, ready-to-commit ResourceBundle:

```
The user has accepted the ProposalBundle below. Produce the final
ResourceBundle. Schema:
{
  "commitFiles": [ {"path": "...", "content": "..."}, ... ],
  "helmChart": { "files": [...], "version": "0.1.0" },
  "terraform": [ {"path": "...", "content": "..."} ] | null,
  "argoApplication": { ... },
  "notes": "string"
}

Every commitFile path MUST start with `.smoothop/`. The Helm chart MUST
lint clean. The ArgoCD Application MUST reference the chart by its
GitHub Packages OCI URL: oci://ghcr.io/<owner>/<repo>/charts/<name>

Output ONLY the JSON.
```

### 5.3 Prompt: `FIX_LOOP`

Sent on ArgoCD failure:

```
ArgoCD reports the following error on the Application below. Produce
an updated ResourceBundle. Schema is identical to MATERIALIZE, plus a
"changes" field summarising what you fixed.

If the optional diff mode is requested (signalled by `"mode": "diff"`
in the user message), emit only the changed files instead of the full
bundle. Each diff entry: {"path": "...", "operation": "create|update|
delete", "content": "..."}.

Output ONLY the JSON.
```

### 5.4 Prompt: `RESUME_CONTEXT`

Sent on crash recovery to remind Opus of the project state without burning tokens by replaying full message history:

```
You are resuming a project after a client-side crash. Below is the
journal snapshot. Treat it as canonical state. Reply with a brief
acknowledgement and the next action you would take (one of:
WAIT_FOR_USER, REDO_LAST_BUNDLE, REQUEST_FIX_LOOP).
```

### 5.5 Prompt: `FALLBACK_GEMINI` (mirror schemas, Gemini-specific framing)

Same schemas as 5.1–5.3, formatted for Gemini Pro 3.1's prompt conventions. Loaded only when the Anthropic channel has failed N times in a window.

---

## 6. Data Model

### 6.1 Local state (Redis keys)

```
project:<projectId>:meta              hash    repo URL, owner, branch, created_at
project:<projectId>:journal           stream  intent/ack events
project:<projectId>:conversation      list    message IDs in order
project:<projectId>:last_bundle       string  JSON ResourceBundle (current source of truth)
project:<projectId>:argo_app_status   hash    last known sync/health state
project:<projectId>:lock              string  exclusive lock during Role 2 operations (TTL 60s)
```

### 6.2 Journal entry shape (Redis Streams XADD)

```json
{
  "ts": "RFC3339Nano",
  "role": "watcher | publisher | router",
  "phase": "intent | start | success | fail",
  "op": "commit | helm-package | helm-publish | argo-upsert | argo-sync | opus-call | gemini-call",
  "ref": "git-sha | chart-version | argo-app-name | opus-message-id",
  "payload_sha256": "string",       // hash of payload; full payload in Redis under :payload:<sha>
  "error": "string | null"
}
```

Idempotency rule: every `op` is keyed by `{projectId, op, ref}`. A `start` without a matching `success | fail` on operator startup triggers retry.

### 6.3 Repo layout owned by operator

```
<user-repo>/
└── .smoothop/
    ├── README.md            # generated; explains the directory
    ├── proposal.json        # latest accepted ProposalBundle
    ├── chart/               # Helm chart source (linted clean)
    │   ├── Chart.yaml
    │   ├── values.yaml
    │   └── templates/
    ├── terraform/           # optional; only if Opus emitted any
    │   └── *.tf
    ├── argo/
    │   └── application.yaml # ArgoCD Application CR
    └── journal-summary.md   # human-readable journal recap
```

Everything outside `.smoothop/` is the user's domain — never written.

---

## 7. UI Requirements (Electrobun)

### 7.1 Visual language

- Monospace everywhere. Pure ASCII components: boxes, tables, trees, graphs.
- Three panes by default: left = conversation history, center = current ProposalBundle visualisation, right = journal tail + ArgoCD status.
- No mouse-only interactions. Every action has a keybinding.

### 7.2 Screens

1. **Init**: user enters repo URL + Anthropic API key + ArgoCD MCP endpoint. Operator probes connectivity.
2. **Investigating**: spinner + live log of repo scan.
3. **Proposal**: ASCII tree of files-to-create, resource graph, "Accept / Edit / Chat" buttons.
4. **Chat**: standard message stream with Opus, plus an inline "current bundle" panel.
5. **Materialize**: progress bar over Role 2 steps (commit → package → publish → argo-upsert → argo-sync).
6. **Watching**: ArgoCD application status with live conditions table.
7. **Fix Loop**: error report + Opus diff preview + "Apply / Reject / Escalate" buttons.
8. **Complete**: handoff summary, ArgoCD Application URL, recommended Day-2 reading.

### 7.3 Operator ↔ UI protocol

- WebSocket over localhost. Token-authed (operator emits a one-time token in the console at boot; UI binds it).
- JSON envelopes: `{type: "event|command|reply", id: "...", payload: {...}}`
- Server-pushed events: `journal_entry`, `opus_chunk`, `bundle_update`, `argo_status`.
- Client commands: `init`, `accept_proposal`, `edit`, `chat`, `materialize`, `abort`, `escalate_to_gemini`.

---

## 8. AI Contract

### 8.1 Primary: Claude Opus 4.7

- Model: `claude-opus-4-7`
- `thinking: {type: "adaptive"}`
- `output_config: {effort: "xhigh"}`
- Prompt caching on static system block (one per role: initial / materialize / fix_loop / resume / gemini-mirror)
- Same conversation thread for the whole project. Implemented by replaying message history from Redis on each call. Beta `compact-2026-01-12` for long-running chains.

### 8.2 Fallback: Gemini Pro 3.1

- Activated when Anthropic fails twice in a 60s window OR returns `service_unavailable`.
- Same schemas; different prompt header tuned for Gemini's conventions.
- Falls back to Opus when Opus reachable again. Conversation IDs aren't shared; the fallback keeps its own thread and the operator stitches outputs.

### 8.3 Diff mode (optional)

Triggered by setting `mode: "diff"` in the Operator → Opus envelope. Opus emits per-file diffs instead of full files. Faster, cheaper, more error-prone. Disabled by default; enabled via `--diff-mode` CLI flag. The `pied-piper` MCP / diff helper agent referenced in v1 can validate diffs before they hit the journal.

---

## 9. ArgoCD Integration

- Use ArgoCD MCP server (community / TBD; flagged as a dependency).
- Operator calls:
  - `applications.create` / `applications.update` — upserts the `Application` CR
  - `applications.sync` — triggers sync
  - `applications.get` / `applications.watch` — polls / streams status
- `Application` shape Opus must emit:
  ```yaml
  apiVersion: argoproj.io/v1alpha1
  kind: Application
  metadata:
    name: <project-slug>
    namespace: argocd
  spec:
    project: default
    source:
      repoURL: oci://ghcr.io/<owner>/<repo>/charts/<name>
      chart: <name>
      targetRevision: 0.1.0
      helm:
        valueFiles: []
        values: |
          # rendered values from Opus
    destination:
      server: https://kubernetes.default.svc
      namespace: <target-ns>
    syncPolicy:
      automated:
        prune: true
        selfHeal: true
      syncOptions:
        - CreateNamespace=true
  ```

- After first stable sync, `syncPolicy.automated` ensures subsequent commits to `.smoothop/` auto-deploy without the operator.

### 9.1 If ArgoCD MCP does not exist

Fallback: operator shells out to `argocd` CLI binary if installed, or calls the ArgoCD HTTP API directly with a per-project API token captured at init. Documented as a degraded mode.

---

## 10. GitHub Integration

### 10.1 Auth

- User provides a fine-scoped PAT or GitHub App installation token at `smoothctl init`. Stored in OS keychain via the OS-native API Electrobun exposes; never written to disk plaintext.
- Required scopes: `repo`, `write:packages`, `read:packages`.

### 10.2 Commits

- All commits authored by `smooth-operator[bot]` with a co-author trailer naming the user (their git config user.email / name).
- One commit per ResourceBundle apply. Commit message follows conventional commit format and references the Opus message ID + journal entry ID.

### 10.3 Helm package publishing

- Each ResourceBundle apply bumps chart version (semver: 0.x.y; bump y on fix-loop, bump x on user-driven edit).
- Publish via `helm push` to `oci://ghcr.io/<owner>/<repo>/charts/<name>`.
- Visibility inherits from the parent repo (private repo → private package).

---

## 11. Persistence & Recovery

### 11.1 Redis topology

- v2 ships with a single Redis instance (Bitnami container or `redis-server` system service). Compose file + lifecycle managed by operator.
- AOF persistence enabled; RDB snapshots every 5 min.
- Single-tenant: one operator process = one Redis = one project at a time.
- Future: Redis Cluster for multi-project parallel use; out of scope for v2.

### 11.2 Journaling protocol

1. Before any side-effecting operation, write `phase=intent`.
2. Begin the operation; write `phase=start`.
3. On completion, write `phase=success` with the result reference.
4. On error, write `phase=fail` with error detail.
5. On startup, scan the journal: any `start` without a matching `success|fail` is replayed (idempotently, because each op is keyed by `{op, ref}` and is safe to re-execute).

### 11.3 Conversation resumption

- Every Opus call's request + response is hashed and stored under `:payload:<sha>` in Redis.
- The conversation list holds ordered message SHAs.
- On resume, the operator replays the conversation list to Opus via the `RESUME_CONTEXT` prompt rather than the full thread, saving tokens.

---

## 12. Security

- Anthropic API key + GitHub PAT + ArgoCD token live only in the OS keychain. Operator binary reads them at startup and zeroes them from process memory after use.
- Operator NEVER writes credentials to disk, journal, or Git.
- Redis bound to `127.0.0.1` only. No exposed network port.
- WebSocket between operator and Electrobun UI uses a one-time bootstrap token printed to stdout.
- All ResourceBundles are validated against the same Kind allowlist + namespace enforcement we already implemented in `sec/hardening` (Section 13 reuses that code).
- Operator's git commits are signed with the user's local SSH/GPG signing config (matches existing user-level rule on signed commits).

---

## 13. What v2 Inherits from v1

Concrete code that stays:

- `internal/policy/` — Kind allowlist, severity, AutoModeAllowed.
- `internal/planner/validator.go` — `EnforceNamespace` + cluster-scoped Kind guard.
- `internal/gitops/` — repo clone, branch, commit, PR (minus the in-cluster context; reused as a local-mode library).
- `internal/llm/` — heavy refactor: still the two-stage chain, but rewritten as a stateless RPC over the static prompt catalog. No CRD watch; no Kubernetes types in scope. The Anthropic SDK wrapper stays.

Concrete code that gets retired:

- All controller-runtime / Kubebuilder scaffolding (`cmd/main.go` becomes a normal `func main()`).
- `api/v1/` CRDs (ChatSession / SmoothAction). Replaced by Redis state + GitHub repo as source of truth.
- In-cluster RBAC, ServiceAccount, leader election, metrics server. Operator runs locally.
- `internal/collector/` and `internal/observability/` — operator no longer watches cluster state directly; ArgoCD does that. Optional metric collection becomes a Day-2 add-on.

---

## 14. Phases & Rollout

### Phase 0 — Skeleton (1 week)

- New `cmd/smoothop/main.go` entrypoint
- Redis bootstrap (embedded container or system service detection)
- Static prompt catalog scaffolding
- Journal write/replay primitives
- No UI, no Opus calls — `go run` prints a journal summary

### Phase 1 — Anthropic chain (1 week)

- Port `internal/llm/` to stateless RPC layer
- Implement `INITIAL_PROPOSAL` + `MATERIALIZE` prompts end-to-end with mocked repo
- ResourceBundle decoder + validator
- httptest-mocked unit tests

### Phase 2 — GitHub + Helm (1 week)

- `internal/gitops/` ported to local-mode (clone, branch, commit, push)
- Helm chart packager (existing template generator already in `internal/gitops/helm.go`)
- GitHub Packages OCI publisher (`helm push oci://...`)
- Idempotent retries via journal

### Phase 3 — ArgoCD MCP (1 week)

- Verify MCP server availability; if none, build raw-HTTP wrapper
- `applications.upsert/sync/watch` flows
- Fix-loop trigger from `OutOfSync|Degraded` observation

### Phase 4 — Electrobun UI (2 weeks)

- Scaffold Electrobun app
- WebSocket protocol with operator
- Eight screens from Section 7.2
- ASCII components library

### Phase 5 — Gemini fallback + diff mode (1 week)

- Gemini Pro 3.1 client + mirror prompts
- Fallback activation policy
- Optional `--diff-mode` flag wired through prompts and validator

### Phase 6 — Hardening + docs (1 week)

- E2E test against a real kind cluster + local ArgoCD
- Threat model review
- User-facing docs / quickstart

Total estimated: **8 weeks** for a single engineer; less with parallel agents (see TASKS-v2.md).

---

## 15. Open Questions

1. **ArgoCD MCP** — does a maintained MCP server exist for ArgoCD? If not, raw HTTP path adds Phase 3 scope. Action: spike in Phase 0.
2. **jules.google.com MCP** — does it exist, and what's its surface? If yes, integrate as the optional first-investigator. If no, drop the dependency.
3. **Electrobun maturity** — is the framework stable enough for OSS distribution? Alternative: Tauri, Wails (Go-native). Action: short spike in Phase 4 kickoff.
4. **Repo layout collision** — what if the user already has a `.smoothop/` folder? Refuse, prompt to move, or use `.smoothop-<projectId>/`? Default: refuse and ask.
5. **Multi-cluster** — single ArgoCD instance can deploy to many clusters. v2 picks the destination cluster from the user's ArgoCD setup; do we let the user pick at init time or always default to `https://kubernetes.default.svc`?
6. **Conversation thread length** — Opus 4.7 has 1M context plus compaction. Do we ever clear the thread? Probably yes on `FirstDeployComplete`. Worth a knob: `--reset-on-handoff`.
7. **Gemini parity** — Gemini Pro 3.1 may not produce JSON of the same fidelity. Fallback may produce degraded bundles. Acceptable in v2?

---

## 16. Out of Scope (v2)

- Multi-user / SaaS
- Day-2 autoscaling/cost advice
- Mid-deploy `kubectl exec`-style debugging
- Direct cluster API calls from the operator (Argo owns that now)
- Custom CRDs for the operator itself (operator is local, no need)
- Skill / agent creation flows for the operator's behaviour at runtime (those happen at build time)

---

## 17. Appendices

### 17.1 Example INITIAL_PROPOSAL user message

```
RepoInvestigationReport
=======================
url: https://github.com/acme/payments-api
default_branch: main
language: Go (1.22)
dockerfile_present: true
dockerfile_entrypoint: /app/payments-api
exposed_ports: 8080
existing_charts: none
existing_terraform: none
ci: GitHub Actions (.github/workflows/ci.yml -> docker build + push to ghcr.io)
detected_dependencies: PostgreSQL (via DATABASE_URL env), Redis (via REDIS_URL env)
test_health_endpoint: /healthz returns 200
notes: README mentions production deploy expects 3 replicas behind a load balancer
```

### 17.2 Example ProposalBundle (Opus output)

```json
{
  "summary": "Deploy as a Helm chart with 3 replicas, internal LoadBalancer Service, HPA on CPU. Postgres + Redis assumed external — supply DATABASE_URL and REDIS_URL via a Secret named payments-api-env.",
  "deploymentMode": "helm",
  "helm": {
    "chartName": "payments-api",
    "values": {"replicaCount": 3, "image": {"repository": "ghcr.io/acme/payments-api", "tag": "latest"}, "service": {"type": "LoadBalancer", "port": 8080}, "autoscaling": {"enabled": true, "minReplicas": 3, "maxReplicas": 10, "targetCPUUtilizationPercentage": 70}}
  },
  "terraform": null,
  "argoApplication": {"name": "payments-api", "namespace": "argocd", "destinationNamespace": "payments-prod"},
  "openQuestions": ["Confirm the target namespace: payments-prod?", "Is the LoadBalancer expected to be internal-only?"],
  "confidence": 0.82,
  "risk": "med"
}
```

### 17.3 ASCII proposal preview (rendered in UI)

```
┌─ payments-api ─────────────────────────────────────┐
│ mode: helm                       confidence: 0.82  │
│ risk: medium                                       │
├────────────────────────────────────────────────────┤
│ .smoothop/                                         │
│ ├── chart/                                         │
│ │   ├── Chart.yaml          ◇ new                  │
│ │   ├── values.yaml         ◇ new                  │
│ │   └── templates/                                 │
│ │       ├── deployment.yaml ◇ new                  │
│ │       ├── service.yaml    ◇ new                  │
│ │       └── hpa.yaml        ◇ new                  │
│ └── argo/                                          │
│     └── application.yaml    ◇ new                  │
├────────────────────────────────────────────────────┤
│ Open questions:                                    │
│  • Confirm target namespace: payments-prod?        │
│  • LoadBalancer internal-only?                     │
└────────────────────────────────────────────────────┘
[A]ccept   [E]dit   [C]hat   [Q]uit
```

### 17.4 Migration note

A migration document (`docs/MIGRATION-v1-to-v2.md`) lives alongside this PRD and tracks how the existing v1 codebase (PRs #2–#5) maps onto v2 components. v2 is a fresh start at the entry point but absorbs most v1 internals as libraries.
