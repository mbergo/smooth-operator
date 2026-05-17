# PRD v2 — Architecture Review

Source: `architect-reviewer` agent pass over `docs/PRD-v2.md` (691 lines) + `docs/TASKS-v2.md` (404 lines). All findings reference PRD line numbers unless otherwise noted.

## Executive Summary

The v2 re-architecture is directionally correct: pulling the LLM out of the cluster removes the worst v1 coupling, and a journaled local executor with static prompts is the right shape. The design **understates** several specific failure modes — particularly journal protocol ordering (intent vs. start), prompt-cache key stability under replay, Helm-publish/git-push partial-commit atomicity, and the user/operator write race on `.smoothop/`. The "five static prompts → reliable cache hits" claim is more fragile than the PRD admits, because conversation replay (§11.3 / T1.2) interacts badly with how Anthropic prompt caching keys.

**Severity legend.** BLOCKER must close before Phase 2 starts. HIGH must close before Phase 3. MEDIUM before GA. LOW is debt.

---

## 1. System design soundness

### 1.1 — HIGH — Watcher/Publisher/Router decomposition over-broad on Watcher

PRD §3.3.1 (L125-127) has the Watcher tracking GitHub commits to `.smoothop/`, ArgoCD `Application.status`, and the Anthropic Messages stream. Three event sources with three different latency / backpressure / failure-mode profiles bundled into a single "role." In practice:

- Anthropic Messages stream is per-call SSE; lifetime = single RPC.
- ArgoCD status is a long-poll or watch stream.
- GitHub `.smoothop/` polling is only useful post-handoff, and §4.4 (L233) says the operator *exits* on handoff. So why is the Watcher watching GitHub commits at all during the operator's lifetime?

The Watcher's GitHub-commit responsibility appears vestigial. Drop it or justify it explicitly.

### 1.2 — HIGH — "Static prompt + journal = idempotency" overclaims

§6.2 L358: *"Idempotency rule: every `op` is keyed by `{projectId, op, ref}`. A `start` without a matching `success | fail` on operator startup triggers retry."*

Fine for `helm-package` (deterministic from inputs) and `argo-upsert` (PUT-shaped). **Not safe** for:

- **`opus-call`** (§6.2 L351 lists it as a journal op). LLM calls are non-deterministic. If the operator retries an `opus-call` whose `start` was logged but `success` was not, it gets a *different* ResourceBundle the second time. Current design treats `ref = opus-message-id`, but on retry that message ID doesn't exist yet — nothing to dedupe against. Burns tokens and silently diverges. **Journal needs a "did the response ever land in Redis under `:payload:`?" lookup before re-issuing, plus an idempotency key passed to Anthropic.** Anthropic supports an `Idempotency-Key` header — wire it.
- **`git-commit`**: depends on whether the commit lands on a fixed branch or one named by content. PRD doesn't say. If commits go to `main` (or any branch with a moving tip), replay either (a) creates an empty commit, (b) creates a duplicate commit, or (c) errors with "nothing to commit." None handled. Either name the branch deterministically (`smoothop/bundle-<sha>`) or write a "commit only if HEAD tree ≠ desired tree" guard.

### 1.3 — BLOCKER — `intent` vs `start` ordering race window

§11.2 (L506-510) describes the protocol as:

```
1. intent  → 2. start  → 3. success|fail
```

This two-step before the side effect is redundant. If `intent` is written but `start` is not, what does that mean on recovery? §6.2 L358 says replay is triggered by *"a `start` without a matching `success | fail`"* — so `intent` without `start` is invisible to recovery. Silent-loss window:

- Operator writes `intent`.
- Operator crashes.
- Operator boots, sees no orphan `start`, concludes "nothing to do."
- Side effect either ran (the world is one step ahead of the journal) or did not (silently dropped).

Either make `intent` the trigger for replay (and document what re-running the intent generator means) OR collapse to a single `start` entry written immediately before the side effect. Two states (`pending`, `done`) suffice. The current three-state protocol with replay-on-`start`-only is incoherent.

### 1.4 — MEDIUM — Lock TTL of 60 s wrong for this workload

§6.1 L341: `project:<id>:lock` with TTL 60 s. Role 2 in §4.1 L182 includes `helm push` to ghcr.io (network), `argo-upsert` (network), and `argo-sync` followed by status polling. Cumulatively that easily exceeds 60 s on a cold network. Lock will expire mid-operation; second boot will think nothing is in flight. Either (a) extend with a heartbeat / lease, or (b) make the lock cover only the journal-write critical section, not the full operation.

---

## 2. External dependency risk (ranked by likelihood)

### 2.1 — BLOCKER — ArgoCD MCP (highest risk)

§3.2 L118 + §9 L434 + §15 Q1: PRD admits "community / TBD; flagged as a dependency." No stable, widely-used ArgoCD MCP server exists at writing. T0.1 spike is appropriately scheduled but **the entire Phase 3 sits on this single spike result.** §9.1 (L469-471) offers a fallback (CLI shell-out or raw HTTPS to ArgoCD API), which is the right answer — but the PRD should commit to **building the raw-HTTP wrapper as primary** and treating MCP as the optional optimization. Inverted dependency. Otherwise Phase 3 has a binary outcome: works, or 2 weeks of unscheduled work.

### 2.2 — HIGH — jules.google.com MCP

§3.2 L119 marks this *optional*, which is correct, and §15 Q2 acknowledges uncertainty. Lower risk than ArgoCD MCP because the fallback (operator-side scan) is on the critical path anyway. **Build operator-side scan first; treat Jules as a Phase 5+ enhancement, not Phase 0.** Don't let it appear in the dependency graph for Phase 1.

### 2.3 — HIGH — Electrobun

§3.2 L112 + §15 Q3: Electrobun is young, single-maintainer, Bun-runtime-coupled. T0.3 explicitly lists Tauri + Wails as backups. Risk is not that Electrobun fails outright; risk is that **OS-keychain access, native window lifecycle, and codesigning** (which the PRD glosses) end up being multi-week yaks. §10.1 L479 says "OS-native API Electrobun exposes" for keychain — **verify Electrobun actually exposes a keychain API.** If not, ship a Go-side `go-keyring` (TASKS T2.1 already references it) and Electrobun is decorative — at which point Wails (Go-native) is cheaper.

### 2.4 — MEDIUM — GitHub Packages OCI registry

Lowest risk of the four; `helm push oci://ghcr.io/...` is mature. One specific gotcha: **chart visibility inheritance** (§10.3 L491) is not how ghcr.io works — packages are *independently* permissioned from their owning repo, with a separate "Inherit access from source repository" toggle that defaults *off*. Document this; users will hit it.

---

## 3. Resumability holes

### 3.1 — BLOCKER — Crash after `git push` before journal `success`

Sequence (§4.1 step 11, L182-186):
1. Write files locally
2. `git commit + git push`
3. `helm package`
4. `helm push`
5. ArgoCD upsert + sync

If a crash happens between step 2 (push lands on GitHub) and the journal `success` for step 2:

- On boot, journal has `start` for `git-push` and no `success`. Replay re-runs.
- Replay = `git push` to the same branch. If the operator's local working copy was wiped (laptop sleep + crash + uncommitted state lost), local HEAD might be different. Replay could **rewind GitHub** (force-push) or **fork the history** (regular push fails fast-forward and the error path is unhandled in §6.2).

PRD §6.2 says ops are "safe to re-execute" but commits are never safe to re-execute without explicit content-addressed semantics. Mitigation: name the commit by **bundle SHA**; on replay, check `git ls-remote` for the SHA already present and skip.

### 3.2 — BLOCKER — Crash during `helm push` to GHCR

OCI registry pushes are layered uploads. A crash mid-push can leave the registry with a manifest pointing at incomplete layers, or — more common in ghcr.io's case — no manifest but layers stored.

TASKS-v2 line 165 says: *"Idempotent retry on `409 already exists`: skip and return existing URL."* That handles the *complete-success-then-replay* case but **not** the *partial-upload-then-replay* case. Need explicit `helm pull` + digest verification before declaring success. Also handle the case where the version was published but the journal `success` never flushed: on replay, `helm push` may 409, and the operator must trust the existing version is the one intended. Tie the chart version to the bundle SHA, not a semver bump on every retry (§10.3 L489-490's bump-on-fix-loop semantics conflict with idempotent retry).

### 3.3 — HIGH — Crash mid-Opus call

See Finding 1.2. The PRD's resumability proof depends on `opus-call` being idempotent. It isn't. Without `Idempotency-Key` (Anthropic supports it) the operator will:
- Pay twice.
- Get two *different* ResourceBundles. Which one wins? Journal says "the first one whose `success` wins" but the first one's body may already be lost (the request was sent, the response was lost, the client crashed without buffering). Operator will **silently retry and get a divergent bundle**, then continue as if it were canonical. Breaks the v2 "deterministic crash recovery" claim.

### 3.4 — MEDIUM — §4.3 and §5.4 contradict on resume strategy

§4.3 L220-222 says resume "replays messages from journal up to last cached message ID." §5.4 `RESUME_CONTEXT` (L318-322) says it sends a *journal snapshot* and asks Opus for one of `{WAIT_FOR_USER, REDO_LAST_BUNDLE, REQUEST_FIX_LOOP}`. **Inconsistent.** Pick one model: (a) Replay the actual message history (which §11.3 L516 then contradicts by saying "rather than the full thread, saving tokens"), or (b) Send a structured snapshot and trust Opus to re-enter from the discriminator. Current PRD is unclear, and the two strategies have totally different cache/cost profiles.

---

## 4. Prompt-cache stability

### 4.1 — HIGH — Replay-from-SHA breaks Anthropic prompt caching

§8.1 L418: *"Same conversation thread for the whole project. Implemented by replaying message history from Redis on each call."*

§11.3 L513-516: *"The conversation list holds ordered message SHAs. On resume, the operator replays the conversation list to Opus via the `RESUME_CONTEXT` prompt rather than the full thread."*

In tension. Worse: Anthropic prompt caching keys on the **literal byte prefix** of the request. Cache hits across calls in a single project require byte-identical system + initial-messages prefix. "Replay from Redis SHAs" only achieves this if:

1. SHAs stored in **insertion order** (PRD says list, good).
2. Retrieved payloads are **byte-identical** to what was originally sent (no whitespace normalization, no re-serialization, no Go `json.Marshal` field-order drift between versions).
3. Static system block uses `cache_control: {"type": "ephemeral"}` — PRD §8.1 says "prompt caching on static system block" but doesn't show the cache breakpoint location.
4. **Same model version** used throughout. Mid-project Anthropic rotation or bump to 4.8 flushes the cache.

Concrete invalidation traps the PRD doesn't address:
- T0.7 says prompts are rendered via `text/template`. Template whitespace handling (`{{-` vs `{{`) drift between versions = cache miss for every call.
- `RESUME_CONTEXT` (a *different* system prompt) used on crash recovery = different cache key from `INITIAL_PROPOSAL` → cache cold-start every resume.
- `compact-2026-01-12` beta (§8.1 L418) **rewrites the conversation** on the server side. Once compaction fires, local SHA list no longer mirrors what the server thinks the conversation is. Next replay either fails or recreates a different cache key.

Recommendation: write a one-page **cache-key invariant doc** alongside the prompt catalog, plus a test in T1.4 that asserts `cache_read_input_tokens > 0` on the **third** call (not the second — second is the only one currently asserted, line 130).

### 4.2 — MEDIUM — Five "static" prompts, but `MATERIALIZE` body is huge

Proposal body interpolated into `MATERIALIZE` (§5.2 L281) includes the full accepted `ProposalBundle`, which is variable. Caching is only effective up to the first variable byte. Make sure the cache breakpoint is *after* the static system+rules block and *before* the body — and the static block is large enough (≥ 1024 tokens) to be cache-eligible per Anthropic's minimums.

---

## 5. Security boundary

### 5.1 — HIGH — One-time WS token necessary but insufficient

§7.3 L404 + §12 L525: one-time bootstrap token, WS over 127.0.0.1.

- **Anyone with local user-process access can read 127.0.0.1 sockets** and the token (printed to stdout — L525, then L404). If the UI is a child process, fine. If stdout goes to a terminal session that's been screen-shared, ssh'd, or tmux-attached, the token leaks. Have Electrobun *receive the token via a Unix-domain socket or anonymous pipe at spawn*, never print it.
- **Token rotation on reconnect**: TASKS T4.2 L243 says "rejected on reuse." But the UI must reconnect after a crash. Either re-derive the token from a keychain entry (then it's not one-time) or require user re-init. PRD doesn't say.

### 5.2 — HIGH — Electrobun attack surface (CSP, devtools, file://)

PRD says zero about Electrobun hardening. Electrobun is a webview wrapper; webviews historically have:
- DevTools accessible in dev builds → token + journal contents visible to anyone with shell access.
- `file://` URL handling that can `fetch()` arbitrary local files if the WV isn't constrained.
- `webSecurity` defaults vary by platform.

Required additions before Phase 6 sign-off (§T6.1 already lists threat model, but specify these):
- Disable devtools in release builds.
- Strict CSP: `default-src 'self'; connect-src ws://127.0.0.1:<port>; script-src 'self'`.
- Disable `file://` access; load UI from embedded asset bundle only.
- Pin webview version; Electron historically gets RCEs in the webview engine.

### 5.3 — HIGH — "Zeroed from memory after use" is theater in Go

§12 L522: *"Operator binary reads them at startup and zeroes them from process memory after use."*

Go strings are immutable; the GC may copy. `[]byte` can be zeroed, but if any code path converts to `string`, it's now in unreachable-but-not-collected memory. The Anthropic SDK takes a `string` API key. **You cannot reliably zero this in Go.** Either:
- Drop the claim and document realistic threat model: "key resident in process memory until exit."
- Use a separate signing subprocess that talks over a pipe (overkill for v2).

Current claim creates a false sense of security and is the kind of line the threat model in T6.1 will burn on.

### 5.4 — MEDIUM — Redis bound to 127.0.0.1 with no authentication

§12 L524: bound to localhost. PRD doesn't mention `requirepass`. Any other process on the user's machine running as the same user can connect. For credential-adjacent data (journal contents may include error logs that reference secret names, ResourceBundles with `valueFiles` paths, Opus message history) this is a hardening gap. Cheap fix: set a random `requirepass` at boot; store in OS keychain.

### 5.5 — MEDIUM — Prompt injection via repo contents

§T6.1 L318 lists this in the threat model. PRD body doesn't acknowledge it. README, Dockerfile comments, `.github/` files are fed into the `INITIAL_PROPOSAL` body verbatim (Appendix 17.1 example shows raw repo content). A hostile repo can instruct Opus to emit a `ResourceBundle` with a `commitFiles` entry outside `.smoothop/`. §5.2 L290 says "MUST start with `.smoothop/`" but enforcement lives in the validator, which must reject before commit. Make this a hard test case in T1.3 L117 (decoder validation).

---

## 6. v1 → v2 migration realism

Inspected the four packages claimed for reuse. Coupling levels:

### 6.1 — HIGH — `internal/planner/validator.go` is k8s-runtime-bound

```
internal/planner/validator.go:27: "sigs.k8s.io/controller-runtime/pkg/client"
internal/planner/validator.go:33: client client.Client
internal/planner/validator.go:37: func NewManifestValidator(client client.Client)
```

Validator takes a controller-runtime `client.Client` as a constructor argument. In v2, no cluster client (operator doesn't talk to k8s — ArgoCD does). Refactor to either remove the client dependency entirely or take a narrow interface that has a no-op implementation locally. **Real refactor, not extraction.** PRD §13 L537 says "EnforceNamespace + cluster-scoped Kind guard" stays — that part is pure, but the wrapping struct isn't.

### 6.2 — HIGH — `internal/policy/` uses `unstructured.Unstructured`

```
internal/policy/engine.go:23: "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
internal/policy/policies.go:24: "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
```

`apimachinery` is fine to keep — it's not controller-runtime — but it pulls in a meaningful chunk of k8s.io modules and forces v2 to keep `k8s.io/apimachinery` as a direct dep even after T0.4's claim (L45) to clean `go.mod` of "controller-runtime / k8s.io dependencies that v2 doesn't use." Be explicit: keep `apimachinery`, drop `client-go` / `controller-runtime`.

### 6.3 — MEDIUM — `internal/gitops/` logger-bound to controller-runtime

```
internal/gitops/git.go:30: "sigs.k8s.io/controller-runtime/pkg/log"
internal/gitops/agent.go:26: "sigs.k8s.io/controller-runtime/pkg/log"
```

`ctrl.Log` is used everywhere. Easy mechanical refactor (swap for `slog`) but every function signature that takes `context.Context` and pulls a logger from it needs updating. TASKS T2.1 L138 doesn't budget for this rename — add it.

Also: `internal/gitops/git.go:143` hardcodes `smooth-operator@k8s.io` as commit author email. v2 PRD §10.2 L484 says author is `smooth-operator[bot]`. Mismatch.

### 6.4 — MEDIUM — `internal/llm/types.go` carries `ChatSessionID`

```
internal/llm/types.go:69: ChatSessionID string
```

`ChatSession` is a v1 CRD (§13 L543 says it's retired). The LLM type still references it. Rename to `ProjectID` (semantic v2 replacement) or make the field generic (`SessionID string`).

PRD §13 L538 says "heavy refactor" of `internal/llm/`. TASKS file (T1.1, L91) underestimates as "pure RPC layer," implying lift-and-shift. Not lift-and-shift; the whole CRD-bound message-mapping logic in `internal/llm/prompt.go` needs to go.

### 6.5 — LOW — `internal/gitops/agent.go` writes commit messages with `ChatSession: %s`

```
agent.go:88: fmt.Sprintf("feat(smooth): %s\n\nGenerated by Smooth Operator\nChatSession: %s", ...)
```

References v1 concept. Note the substring "Generated by" — global git rules in this repo (`/home/mbergo/.claude/rules/signed-commits-that-does-not-include-claude.instructions.md`) forbid "generated by" in commit messages. v1 code already violates the user's commit-author rule; v2's refactor is the chance to fix. PRD §10.2 L484 doesn't currently say.

---

## 7. Operator vs ArgoCD steady-state coherence

### 7.1 — BLOCKER — User-edit race on `.smoothop/` + ArgoCD auto-sync flap

§3.3.2 L131: operator commits `.smoothop/` contents. §6.3 L378: *"Everything outside `.smoothop/` is the user's domain — never written."* Implication: inside `.smoothop/` is the operator's domain.

§4.2 (fix loop) L197-209 happens *while the operator is still running.* The PRD never says the operator owns an *exclusive lock* on `.smoothop/`. User scenarios that break:

- User opens `.smoothop/chart/values.yaml` in their editor, tweaks replica count. Operator simultaneously runs `FIX_LOOP`, gets a new bundle from Opus, overwrites `values.yaml`, commits. User's edit is **silently lost** (Opus didn't see it; it's not in the conversation).
- User commits a manual change. Operator's next `git push` either fast-forwards over it or fails. If it force-pushes (PRD does not specify push semantics), user's commit vanishes.
- ArgoCD auto-sync has already fired on the *prior* bundle. Operator pushes new bundle. Argo syncs the new one. ArgoCD races with operator's fix-loop watcher.

Mitigations (pick at least two):
- During an active operator session, the `.smoothop/` folder is operator-owned: warn the user, ideally `chmod a-w` the directory while the process runs.
- All operator pushes go to a `smoothop/bundle-<n>` branch; the user-visible `main` only gets a final fast-forward merge at `FirstDeployComplete`. Decouples fix-loop chaos from ArgoCD's watched branch.
- Disable ArgoCD `syncPolicy.automated` until handoff. PRD §9 L466 turns it on from the *first* application creation — meaning Argo will auto-sync every fix-loop intermediate. That's not just a race; it's deliberate flapping. **Turn auto-sync on at handoff, not at first deploy.**

### 7.2 — HIGH — §4.4 handoff has no fence

L229-237 describe handoff as "operator commits a README + exits." No instruction to enable `syncPolicy.automated` *at handoff* (because §9 already enabled it). No flag in Argo to say "operator gone, you own this now." If the operator crashes *after* the handoff README commit but *before* journaling "Handoff," what happens on next launch? PRD says (§4.3) the journal resumes. But nothing left to resume. Need an explicit terminal journal state (`closed` / `handed_off`) that suppresses any retry.

---

## 8. PRD §15 Open Questions — blocking vs nice-to-have

| # | Question | Blocking? | When to resolve |
|---|----------|-----------|----------------|
| 1 | ArgoCD MCP existence | **BLOCKER** | Phase 0 (T0.1) — already scheduled, but invert the default: build raw-HTTP first, MCP optional. |
| 4 | Repo layout collision (`.smoothop/` already exists) | **BLOCKER** | Phase 0 — trivial to decide ("refuse, ask"), zero code cost, prevents data loss. Pick now. |
| 6 | Conversation thread reset on handoff | **HIGH** | Phase 1 — interacts with prompt caching (Finding 4.1). Decide alongside cache-key invariant doc. Recommend: thread closes on handoff; new project = new thread. |
| 5 | Multi-cluster destination | MEDIUM | Phase 3 — needed before T3.1 client adapter is final. Default to user-pick at init; fallback to `kubernetes.default.svc` only when ArgoCD reports a single registered cluster. |
| 3 | Electrobun maturity | MEDIUM | Phase 4 (T0.3 spike) — already scheduled. Add explicit keychain-access verification (Finding 2.3). |
| 7 | Gemini parity / degraded bundles | MEDIUM | Phase 5 — acceptable to ship with "Gemini-mode shows a warning badge; bundles get extra validator strictness; user can re-prompt via Anthropic when restored." |
| 2 | Jules MCP | LOW | Defer to post-v2. Building it into the critical path early adds dependency risk for zero user-visible benefit (the operator scan is already on the critical path). |

---

## 9. Summary table

| # | Severity | Topic | PRD ref |
|---|----------|-------|---------|
| 1.1 | HIGH | Watcher has three unrelated responsibilities; GitHub-commit watching is vestigial | §3.3.1 L125-127, §4.4 L233 |
| 1.2 | HIGH | `opus-call` is not idempotent; journal replay will silently diverge | §6.2 L351-358 |
| 1.3 | **BLOCKER** | `intent`-without-`start` race window invisible to recovery | §11.2 L506-510, §6.2 L358 |
| 1.4 | MEDIUM | 60 s lock TTL shorter than Role 2 critical section | §6.1 L341 |
| 2.1 | **BLOCKER** | ArgoCD MCP non-existence puts Phase 3 on a single spike outcome | §3.2 L118, §9 L434, §15 Q1 |
| 2.2 | HIGH | Jules MCP dependency adds risk for zero critical-path value | §3.2 L119, §15 Q2 |
| 2.3 | HIGH | Electrobun keychain access unverified; release-build hardening unspecified | §3.2 L112, §10.1 L479, §15 Q3 |
| 2.4 | MEDIUM | ghcr.io chart visibility doesn't inherit by default | §10.3 L491 |
| 3.1 | **BLOCKER** | Crash after `git push` before `success` can fork/rewind GitHub history | §4.1 L182-186, §6.2 L358 |
| 3.2 | **BLOCKER** | Crash mid-`helm push` leaves OCI in indeterminate state; 409-handling insufficient | §4.1 L186, T2.3 L165 |
| 3.3 | HIGH | Mid-Opus-call crash without `Idempotency-Key` causes silent bundle divergence | §6.2 L351, §8.1 L412-418 |
| 3.4 | MEDIUM | §4.3 and §5.4 contradict on resume strategy (replay vs snapshot) | §4.3 L220-222, §5.4 L318-322, §11.3 L516 |
| 4.1 | HIGH | SHA-replay conversation is not byte-stable; cache will miss silently | §8.1 L418, §11.3 L513-516 |
| 4.2 | MEDIUM | MATERIALIZE variable body cap on cache effectiveness unaddressed | §5.2 L281 |
| 5.1 | HIGH | One-time WS token printed to stdout leaks via terminal session capture | §7.3 L404, §12 L525 |
| 5.2 | HIGH | Electrobun CSP/devtools/file:// hardening unspecified | §7, §12 |
| 5.3 | HIGH | "Zero credentials from memory" is unachievable in Go with SDK string args | §12 L522 |
| 5.4 | MEDIUM | Redis local-bind without password = same-user-process leakage | §12 L524 |
| 5.5 | MEDIUM | Prompt injection via repo contents not enforced in critical path | §5.2 L290, T1.3 L117 |
| 6.1 | HIGH | `internal/planner/validator.go` constructor takes `controller-runtime` client; not a library | (file inspected) |
| 6.2 | HIGH | `internal/policy/` keeps `apimachinery` dep; T0.4's `go.mod` claim overpromises | (file inspected), T0.4 L45 |
| 6.3 | MEDIUM | `internal/gitops/` is logger-bound to `ctrl.Log`; needs mechanical refactor | (file inspected), T2.1 L138 |
| 6.4 | MEDIUM | `internal/llm/types.go` still carries `ChatSessionID`; v1 CRD vestige | (file inspected), T1.1 L91 |
| 6.5 | LOW | v1 commit-message template uses forbidden "Generated by" phrasing | (file inspected), §10.2 L484 |
| 7.1 | **BLOCKER** | ArgoCD `syncPolicy.automated` on from first deploy = flap during fix loop; user-edit race | §9 L460-465, §3.3.2, §6.3 L378 |
| 7.2 | HIGH | No terminal "handed-off" journal state; replay-after-handoff undefined | §4.4 L229-237 |
| 8 | — | Open questions: #1 + #4 blocking; #6 + #3 + #5 high; #2 deferable; #7 medium | §15 |

---

## 10. Minimum PRD changes before Phase 1 starts

1. **Rewrite §11.2** to a two-state protocol (`pending`/`done`) or define recovery semantics for `intent`-only entries.
2. **Add §6.2 invariant**: every journal `op` must include an externally-supplied idempotency key (bundle SHA for git, content digest for OCI, `Idempotency-Key` for Anthropic). Drop `opus-call` from the "safe to re-execute" set without this key.
3. **§9 amendment**: `syncPolicy.automated` is **disabled** until `FirstDeployComplete`. Operator enables it as part of handoff. Fix loop runs against a manually-triggered sync, not auto-sync.
4. **§4.4 amendment**: add terminal journal state (`handed_off`). Resume logic short-circuits on this state.
5. **§12 amendment**: drop the "zero credentials from memory" claim; add explicit Electrobun CSP/devtools/file:// requirements; Redis `requirepass` at boot; token delivery via pipe not stdout.
6. **§15 ordering**: resolve Q1 + Q4 now; defer Q2; budget Q3 keychain verification into T0.3 DoD.
7. **§13 amendment**: be explicit about which sub-packages remain coupled to k8s libraries after the refactor (apimachinery: yes; controller-runtime: no; ctrl.Log: no).
8. **§8.1 amendment**: declare cache-key invariants (byte-identical system prefix, fixed cache-control breakpoint, deterministic template rendering, behavior when `compact-2026-01-12` fires).
