# Smooth Operator — PRD v2

**One-liner:** A local-first deployment companion. User points it at a GitHub repo; the operator investigates, drafts a deployment plan, hands the conversation to Claude Opus 4.7 inside a native TUI dashboard until consensus, then commits the resulting plain Kubernetes manifests plus an ArgoCD `Application` CRD to a folder it owns in the repo, registers the deployment with ArgoCD via its HTTPS API, and watches the rollout — routing any failure back through Opus until the deployment stabilizes. After first deploy, the operator bows out; ArgoCD takes over the steady-state reconciliation.

---

## 0. Document Control

- **Owner:** Platform
- **Version:** v2.1 (amended)
- **Status:** For review
- **Supersedes:** `PRJ.md` (v1 — in-cluster operator with embedded AI)

### 0.1 Amendment Log

**v2.1 (this revision).** Incorporates the architect-reviewer pass (`docs/PRD-v2-review.md`) and the ArgoCD-directly-consumes-plain-manifests realization. Major changes:

1. **Drop Helm from the critical deploy path.** ArgoCD's `Application` CRD consumes plain YAML directories natively. Operator emits final-form manifests into `.smoothop/manifests/` + a single `Application` CRD pointing at that directory. No chart packaging, no OCI registry, no version-bump dance. Helm chart generation moves to optional Phase 5+ enhancement for users who want to redistribute their deployment. Kills review BLOCKERs 3.2 and HIGH 2.4.
2. **Two-state journal protocol.** Collapse `intent / start / success | fail` to `pending / done`. Replay scans for `pending` without matching `done`. Closes review BLOCKER 1.3.
3. **Idempotency keys are mandatory.** Every journal `op` carries an externally-supplied idempotency key: bundle SHA for git, `Idempotency-Key` header for Anthropic. `opus-call` drops from the "safe to re-execute" set without this key. Closes review HIGH 3.3.
4. **Content-addressed git commits.** Operator commits to a `smoothop/bundle-<sha>` branch named by content hash. Replay checks `git ls-remote` for the SHA; skips if present. Closes review BLOCKER 3.1.
5. **ArgoCD auto-sync deferred to handoff.** `syncPolicy.automated` is disabled during the fix loop; operator triggers syncs manually. Auto-sync enables at the `handed_off` terminal state. Closes review BLOCKER 7.1.
6. **Terminal journal state.** Add `handed_off` state; resume logic short-circuits on it. Closes review HIGH 7.2.
7. **ArgoCD HTTPS primary, MCP optional.** Inverted dependency. Phase 3 ships against raw HTTPS regardless of MCP availability. Closes review BLOCKER 2.1.
8. **`.smoothop/` is operator-owned during session.** Operator warns + advisory-locks the directory while running. User edits inside `.smoothop/` are surfaced to the chat, not silently overwritten.
9. **Security tightening.** Drop the "zeroed from memory" claim (unachievable in Go with `string` SDK args). Add Redis `requirepass`. Token delivered to UI via pipe, not stdout. Strict CSP + devtools off + no `file://` for Electrobun. Closes review HIGH 5.1 / 5.2 / 5.3 + MEDIUM 5.4.
10. **Prompt-cache invariants.** Section 8 adds a cache-key invariant subsection: byte-identical system prefix, fixed cache-control breakpoint, deterministic `text/template` rendering, behaviour when `compact-2026-01-12` fires. Closes review HIGH 4.1.

The two-stage Reasoner → Generator chain from the original PRD survives; only the bundle shape and the deploy path shift.

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
- If terminal entry is "handed_off" -> refuse to resume; print the
  Application URL and exit. The project is owned by ArgoCD now.
- If journal has pending entries without matching done -> resume:
   1. Re-read all journal entries for the project
   2. Reconstruct in-memory state (current ResourceBundle, last Opus
      message ID, ArgoCD app state)
   3. Hot path A or cold path B from §11.3 (depending on whether the
      same operator process or a fresh one is resuming)
   4. Continue from the failed step (idempotent retry of Role 2 ops
      keyed by the journal entry's idempotency key)
- User is never re-prompted for input that was already captured
```

### 4.4 Handoff to ArgoCD (operator exits)

Handoff is the **terminal state** of the project journal. Once written, the operator refuses to resume the project on subsequent launches (see §4.3).

```
On FirstDeployComplete (Application Synced + Healthy AND no pending
journal entries):

1. Operator triggers one final argo sync to confirm steady state.
2. Operator patches the Application to enable syncPolicy.automated:
   {prune: true, selfHeal: true}.
3. Operator merges smoothop/bundle-<sha> into the user's default
   branch via fast-forward (no force-push, no rewrite).
4. Operator commits a final README.md to .smoothop/ explaining
   the ArgoCD Application, how to evolve it, and how to remove the
   .smoothop/ folder cleanly if desired.
5. Operator writes one final journal entry: phase=done, op=handed_off,
   payload={appURL, defaultBranchTipSha}.
6. Operator process exits.
7. ArgoCD continues to reconcile the Application via normal GitOps.
   Future image deploys = developer pushes new image tag -> CI/CD
   updates the manifest in .smoothop/manifests/ -> ArgoCD auto-syncs.
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

Sent after consensus. Asks Opus to produce the final, ready-to-commit ResourceBundle.

The bundle is a flat list of files the operator commits to `.smoothop/`. The ArgoCD `Application` CRD points its `source.path` at `.smoothop/manifests/`, telling ArgoCD to render every YAML file in that directory as plain manifests (no Helm, no Kustomize). One bundle = one git commit = one deploy state.

```
The user has accepted the ProposalBundle below. Produce the final
ResourceBundle. Schema:
{
  "commitFiles": [
    {"path": ".smoothop/manifests/<name>.yaml", "content": "..."},
    {"path": ".smoothop/argocd/application.yaml", "content": "..."},
    {"path": ".smoothop/terraform/<name>.tf", "content": "..."}  // optional
  ],
  "notes": "string"
}

Rules:
- Every commitFile path MUST start with `.smoothop/`.
- Plain Kubernetes manifests go under `.smoothop/manifests/` — one
  resource per file, named `<kind>-<name>.yaml`.
- Exactly one ArgoCD Application CRD goes at
  `.smoothop/argocd/application.yaml`. Its spec.source MUST be:
    source:
      repoURL: <user's repo URL>
      targetRevision: smoothop/bundle-<bundleSha>
      path: .smoothop/manifests
      directory:
        recurse: true
- spec.syncPolicy MUST NOT include automated.* on the initial bundle.
  Auto-sync is enabled by the operator only at handoff.
- Optional Terraform under `.smoothop/terraform/`. No interpolation
  syntax in Terraform files that depends on operator runtime state.
- All Secrets are referenced by name (created out-of-band).
- Bundle MUST be self-contained: every selector, label, and Service
  port referenced by one file is defined in another file in the bundle.

Output ONLY the JSON object.
```

Helm-chart packaging is intentionally out of scope here. ArgoCD reads plain manifests directly; chart authoring is a Phase 5+ optional output for users who want to redistribute their deployment.

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
- Every Messages API call carries an `Idempotency-Key` header set to `sha256(system || conversation || user-body)` so replay after a mid-call crash reuses the same response (closes review HIGH 3.3).

### 8.1.1 Cache-key invariants (closes review HIGH 4.1)

Anthropic prompt caching keys on the literal byte prefix of the request. The following invariants are enforced by the operator and verified by unit tests:

1. **System prefix is byte-frozen at compile time.** Each prompt template's system block is rendered once at build via `text/template` with no per-session interpolation, embedded into the binary via `embed.FS`. Whitespace (`{{-` vs `{{`) and field order in struct-derived sections are normalised at code-review time.
2. **One cache breakpoint per prompt.** `cache_control: {"type": "ephemeral"}` is set on the LAST `TextBlockParam` of the system array, never inside the user message. Variable content (the body) lives strictly after the breakpoint.
3. **Replay is byte-identical.** The conversation list stores ordered message SHAs; payloads under `:payload:<sha>` are written once and never re-serialised. The operator reconstructs the message slice from the same bytes that were originally sent — no Go `json.Marshal` round-trip on replay.
4. **Same model version for the project's lifetime.** The model string is captured at project init and persisted in `project:<id>:meta:model`. Mid-project bumps require explicit user opt-in and reset the cache.
5. **Compaction handling.** When `compact-2026-01-12` fires, the server's view of the conversation diverges from the local SHA list. The operator detects compaction via the response's `compaction` block, snapshots the new server-side state into `:payload:<compaction-sha>`, and prunes preceding SHAs from the conversation list. Subsequent calls replay from the compaction snapshot forward. A unit test asserts `cache_read_input_tokens > 0` on the third call after compaction.
6. **`RESUME_CONTEXT` has its own cache key.** Cold resume (§11.3 path B) is a cache miss by design. Hot resume (§11.3 path A) reuses the original role's cache key.

### 8.2 Fallback: Gemini Pro 3.1

- Activated when Anthropic fails twice in a 60s window OR returns `service_unavailable`.
- Same schemas; different prompt header tuned for Gemini's conventions.
- Falls back to Opus when Opus reachable again. Conversation IDs aren't shared; the fallback keeps its own thread and the operator stitches outputs.

### 8.3 Diff mode (optional)

Triggered by setting `mode: "diff"` in the Operator → Opus envelope. Opus emits per-file diffs instead of full files. Faster, cheaper, more error-prone. Disabled by default; enabled via `--diff-mode` CLI flag. The `pied-piper` MCP / diff helper agent referenced in v1 can validate diffs before they hit the journal.

---

## 9. ArgoCD Integration

### 9.1 Transport

**Primary: raw HTTPS against the ArgoCD API server**, using a per-project API token captured at init and stored in the OS keychain. Operator calls land at `https://<argocd-host>/api/v1/applications/...`.

**Optional MCP path.** If a vetted ArgoCD MCP server is available at runtime, the operator switches to it for the same call set. Decided by the `argocd.transport = "https" | "mcp"` config knob. MCP is an optimisation, not a dependency. Phase 3 ships raw-HTTPS regardless of MCP availability.

### 9.2 Calls

- `PUT /api/v1/applications/{name}` — upsert the `Application` CR.
- `POST /api/v1/applications/{name}/sync` — trigger sync (manual during fix loop; one-shot at handoff just before enabling auto-sync).
- `GET /api/v1/applications/{name}` — poll status.
- `GET /api/v1/stream/applications?name={name}` — SSE watch on status transitions.

### 9.3 Application shape Opus emits

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: <project-slug>
  namespace: argocd
  finalizers:
    - resources-finalizer.argocd.argoproj.io
spec:
  project: default
  source:
    repoURL: <user's repo URL>            # NOT an OCI registry
    targetRevision: smoothop/bundle-<sha> # content-addressed branch
    path: .smoothop/manifests             # plain YAML directory
    directory:
      recurse: true
  destination:
    server: https://kubernetes.default.svc
    namespace: <target-ns>
  syncPolicy:
    # automated.* INTENTIONALLY ABSENT during fix loop.
    # Operator enables auto-sync at handoff, see §4.4.
    syncOptions:
      - CreateNamespace=true
      - ApplyOutOfSyncOnly=true
    retry:
      limit: 5
      backoff:
        duration: 5s
        factor: 2
        maxDuration: 3m
  revisionHistoryLimit: 10
  ignoreDifferences: []                   # populated by Opus if HPAs etc.
```

### 9.4 Auto-sync lifecycle (closes review BLOCKER 7.1)

Auto-sync is **off** during the fix loop. The operator triggers each sync manually after committing a new bundle. Rationale: with auto-sync on, ArgoCD would race the operator and apply intermediate fix-loop bundles before they're verified.

At the `handed_off` terminal state (§4.4), the operator issues one final `PATCH /api/v1/applications/{name}` to add:

```yaml
syncPolicy:
  automated:
    prune: true
    selfHeal: true
```

After that patch, ArgoCD owns reconciliation. Operator exits.

---

## 10. GitHub Integration

### 10.1 Auth

- User provides a fine-scoped PAT or GitHub App installation token at `smoothctl init`. Stored in OS keychain via the operator's keychain library (`go-keyring`); never written to disk plaintext. Electrobun never sees the token.
- Required scopes: `repo` (read + write to `.smoothop/`, branch creation). `write:packages` only required if optional Helm publishing in §10.3 is enabled.

### 10.2 Commits (closes review BLOCKER 3.1)

- All commits authored by `smooth-operator[bot]` with a co-author trailer naming the user (their git config user.email / name). Commit message format is conventional-commit and references the Opus message ID + journal entry ID. **Commit messages must not contain the substring "generated by", "co-authored-by claude", or any other AI attribution** (see user-level rule on signed commits).
- **Content-addressed branches.** Each bundle commits to `smoothop/bundle-<bundleSha>`, where `bundleSha = sha256(canonical-json(commitFiles))`. Replays check `git ls-remote origin smoothop/bundle-<bundleSha>` first; if the branch exists at the expected tip, skip the push.
- `main` (or the user's default branch) is never written by the operator during fix loop. The final `handed_off` step merges the converged `smoothop/bundle-<sha>` into the default branch via fast-forward.
- Commits are signed using the user's local SSH/GPG signing config.

### 10.3 Optional: Helm chart publishing (Phase 5+)

Plain manifests under `.smoothop/manifests/` are the v2 deploy artifact. ArgoCD consumes them directly via `source.directory`. There is **no Helm packaging on the critical path**.

A Phase 5+ enhancement may translate the bundle into a Helm chart and publish via `helm push oci://ghcr.io/<owner>/<repo>/charts/<name>` for users who want to redistribute their deployment. When enabled, the chart version is derived from the bundle SHA (`0.0.0-<short-sha>`), not a manually bumped semver — that keeps `helm push` idempotent under journal replay.

---

## 11. Persistence & Recovery

### 11.1 Redis topology

- v2 ships with a single Redis instance (Bitnami container or `redis-server` system service). Compose file + lifecycle managed by operator.
- AOF persistence enabled; RDB snapshots every 5 min.
- Single-tenant: one operator process = one Redis = one project at a time.
- Future: Redis Cluster for multi-project parallel use; out of scope for v2.

### 11.2 Journaling protocol (closes review BLOCKER 1.3 + HIGH 3.3)

**Two states only: `pending` and `done`.**

1. Before any side-effecting operation, write `phase=pending` with the typed payload AND a stable **idempotency key**. Required keys per op:
   - `git-commit`: `bundleSha = sha256(canonical-json(commitFiles))`
   - `git-push`: `bundleSha` (same as above)
   - `opus-call`: `requestSha = sha256(system || conversation || user-body)` — passed as Anthropic's `Idempotency-Key` header so a replay reuses the same response, no extra spend
   - `argo-upsert`: `appName || bundleSha`
   - `argo-sync`: `appName || bundleSha`
2. Execute the operation.
3. On success, write `phase=done` with the result reference and the same idempotency key.
4. On error, write `phase=done` with `error` populated.

**Replay on startup**: scan the journal for `pending` entries with no matching `done`. For each:

- Look up the idempotency key in Redis (`:idem:<op>:<key>` → `done` payload if present).
- If `done` payload is present, the op succeeded but the journal didn't flush. Promote the `pending` entry to `done` using the cached result.
- Otherwise, re-execute the op. Because every op accepts the idempotency key (Anthropic header, git `ls-remote` precheck, ArgoCD PUT shape), re-execution is safe.

There is no `intent` phase. The single `pending` entry is written **before** the side effect and **after** the idempotency key is computed. A crash before `pending` lands means the side effect did not run — nothing to replay. A crash after `pending` but before `done` triggers replay, which is safe.

**Lock TTL: lease, not 60 s fixed.** The project lock uses a heartbeat — the operator renews the lease every 10 s while a side-effecting op is in flight. Lock auto-expires only on operator death, not on long-running network calls (closes review MEDIUM 1.4).

### 11.3 Conversation resumption (closes review MEDIUM 3.4 + HIGH 4.1)

**Two distinct paths**, resolved here to remove the §4.3 / §5.4 contradiction in the original draft:

**A. Hot resume — same process, same model session.** Conversation is replayed byte-identically from `:payload:<sha>` Redis entries. SHAs are stored in insertion order. The static system prefix is byte-frozen at startup and cache-keyed via `cache_control: {type: "ephemeral"}`. This path preserves Anthropic prompt-cache hits.

**B. Cold resume — operator crashed mid-flow, restarted later.** Operator sends a **single** `RESUME_CONTEXT` prompt containing a structured journal snapshot. Opus replies with a discriminator: `WAIT_FOR_USER`, `REDO_LAST_BUNDLE`, or `REQUEST_FIX_LOOP`. The operator then re-enters the normal flow with the `INITIAL_PROPOSAL` / `MATERIALIZE` / `FIX_LOOP` system prefix — accepting a one-time cache miss because the cost of re-establishing context this way is far less than replaying the full hot conversation.

The choice between A and B is made by checking whether the operator process owns the in-memory Redis connection from the original run; if yes, A; if no, B.

---

## 12. Security

### 12.1 Credentials (closes review HIGH 5.3 + MEDIUM 5.4)

- Anthropic API key + GitHub PAT + ArgoCD token live in the OS keychain via `go-keyring`. Operator reads them at startup and passes them to the SDK / HTTP client.
- **Threat model is realistic, not aspirational**: credentials remain resident in process memory for the lifetime of the operator. Go strings are immutable and the Anthropic / HTTP SDKs accept `string`, so we cannot reliably zero them. Mitigation is operator lifetime, not memory hygiene — the operator exits as soon as `handed_off` is reached.
- Operator NEVER writes credentials to disk, journal, or Git.
- Redis bound to `127.0.0.1` only AND configured with `requirepass <random-32-byte-hex>` generated at boot. The password is stored in the OS keychain alongside the API tokens. Same-user processes therefore cannot connect without keychain access.

### 12.2 UI ↔ operator transport (closes review HIGH 5.1)

- WebSocket between operator and Electrobun UI runs on `127.0.0.1:<ephemeral-port>` and requires a one-time bootstrap token.
- **Token delivery is via anonymous pipe**, not stdout. Operator spawns Electrobun as a child process; the token is written to the child's stdin (or to a Unix-domain socket if pipe semantics are insufficient on the host OS).
- Token rotation on reconnect: stored under `project:<id>:wsToken` in Redis with 5-minute idle TTL; refreshed on every successful frame. UI reconnect after crash uses the stored token if the operator is still alive; otherwise reconnect requires user re-init.

### 12.3 Electrobun hardening (closes review HIGH 5.2)

- Devtools disabled in release builds. Builds with devtools enabled refuse to run if `ANTHROPIC_API_KEY` is present in the environment.
- Strict CSP: `default-src 'self'; connect-src ws://127.0.0.1:<port>; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:`.
- UI loaded from an embedded asset bundle. `file://` URLs are not permitted; the webview is configured to deny them.
- Webview version pinned in the release manifest. Auto-update of the webview is gated on a CI re-test of the smoke suite.

### 12.4 Bundle validation (carries forward v1 work)

- All ResourceBundles are validated against the same Kind allowlist + namespace enforcement implemented in `sec/hardening` (Section 13 reuses that code).
- Every `commitFile.path` MUST start with `.smoothop/` and MUST NOT contain `..` segments or null bytes (closes review MEDIUM 5.5).
- `metadata.namespace` on every manifest MUST equal the destination namespace declared in the `Application` CR (carries forward v1's `EnforceNamespace`).

### 12.5 Commit signing

- Operator's git commits are signed with the user's local SSH/GPG signing config (matches user-level rule on signed commits).
- Commit message scanner rejects any string matching the AI-attribution token list before pushing (matches user-level rule).

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

### Phase 2 — GitHub publisher (3 days, down from 1 week)

- `internal/gitops/` ported to local-mode (clone, content-addressed branch creation, commit, push)
- Content-addressed branch name `smoothop/bundle-<sha>` with `git ls-remote` skip-if-present check
- Idempotent retries via journal
- No Helm packager. No OCI registry. Plain manifests under `.smoothop/manifests/` are the v2 deploy artifact.
- (Optional Helm chart publishing moves to Phase 5+ for users who want redistribution.)

### Phase 3 — ArgoCD HTTPS client (1 week)

- Raw HTTPS against the ArgoCD API server is the **primary** path. MCP is optional.
- `applications.upsert/sync/watch` flows against `/api/v1/applications/...`
- Per-project ArgoCD API token captured at init
- Fix-loop trigger from `OutOfSync|Degraded` observation via SSE watch
- Optional MCP-transport switch behind a runtime config knob

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

Resolved in v2.1 (this revision):

- ~~**Q1 ArgoCD MCP**~~ — **resolved.** Raw HTTPS is the primary transport (§9). MCP is an optional optimisation. Phase 3 no longer depends on MCP existing.
- ~~**Q4 Repo layout collision**~~ — **resolved.** If `.smoothop/` already exists, operator refuses and prompts the user to remove it or move it manually. Never auto-renames.

Still open (ranked by blocking potential per reviewer §8):

1. **Conversation thread reset on handoff** (was Q6, now HIGH). Recommend: thread closes on handoff; new project = new thread. Confirms cache-key invariant 4 in §8.1.1.
2. **Multi-cluster destination** (was Q5, MEDIUM). Default to user-pick at init; if ArgoCD reports exactly one registered cluster, default to that one. Falls back to `https://kubernetes.default.svc` only when ArgoCD is co-located with the target cluster.
3. **Electrobun maturity** (was Q3, MEDIUM). Phase 0 spike T0.3 decides. Add explicit keychain-access verification to the spike's DoD: if Electrobun cannot read the OS keychain, the operator handles credentials Go-side via `go-keyring` and Electrobun is decorative.
4. **Gemini parity** (was Q7, MEDIUM). Acceptable for v2 to ship with: Gemini-mode shows a warning badge in the UI; bundles get extra validator strictness; user can re-prompt via Anthropic when restored.
5. **jules.google.com MCP** (was Q2, LOW). **Defer to post-v2.** Operator-side repo scan is on the critical path regardless; Jules adds risk for zero user-visible benefit in v2.

New questions surfaced by the v2.1 amendment:

6. **`.smoothop/` advisory lock semantics.** During an active operator session, should the operator `chmod a-w .smoothop/`, or rely on a `.smoothop/LOCK` sentinel file + Git hook warnings? Trade-off: filesystem permissions are stronger but break editor UX; sentinel files are politer but easier to ignore. Default: sentinel file + visible warning in the TUI.
7. **`smoothop/bundle-<sha>` branch retention.** After fast-forward into the default branch at handoff, do we delete the per-bundle branches or keep them as a recovery history? Default: keep last 10 per project; configurable.

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
