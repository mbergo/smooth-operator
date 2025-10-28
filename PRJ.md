Short version: the **Chat UI** will **generate and apply** `ChatSession` CRDs directly to the cluster. **Smooth Operator** watches those CRDs, collects deployment + metrics context, calls the LLM (OpenAI or similar) to infer what the project *needs*, then either asks the user in the UI or (if auto mode is enabled and policies allow) implements the fixes itself. After the deployment stabilizes, Smooth Operator generates/updates charts and commits them to Git (branch/PR or direct commit).

---

## 1 — Key change

- **UI responsibility:** convert user conversation + charts + metadata into a `ChatSession` CRD and apply it to the Kubernetes API (the UI acts like a client to the cluster).
- **Operator responsibility:** watch `ChatSession` CRDs, analyze cluster state and supplied artifacts, run LLM inference, propose or apply changes, then produce artifacts (charts/Helm/Kustomize) and push them to Git.

This makes the UI the source of truth for intent (CRD), preserves Kubernetes auditability, and keeps the operator purely reactive and in-cluster.

---

## 2 — Updated high-level flow (step-by-step)

1. **User → Chat UI**
    - User types prompt and optionally attaches charts, metric links, images or selects dashboards.
    - UI packages: `user_prompt`, `metadata` (git repo/path, namespace, preferAuto flag), pointers or base64 blobs of charts/metrics.
2. **UI → Kubernetes**
    - UI **applies** a `ChatSession` CRD to the cluster (POST to k8s API). This CRD is the declared intent.
3. **Operator (watcher)** sees the `ChatSession` CRD → begins context collection:
    - Reads Deployments/Pods/Services/Ingress in the `targetNamespace`.
    - Fetches Prometheus/Grafana metrics, logs (Loki), and the supplied chart artifacts.
    - Detects whether a new Deployment was just created/modified and whether it *should* have things like HPA, Service type LB, PVC, probes, etc.
4. **LLM inference**
    - Operator builds a structured prompt (YAML snippets, metrics snapshot, policy constraints, chart descriptions) and sends to OpenAI (or another LLM).
    - LLM returns a structured plan: inferred needs, suggested patches, confidence & rationale.
5. **Operator decision**
    - If **Suggest Mode** (default): Operator creates a `SmoothAction` CRD that contains the diff, generated YAML, rationale, and risk/confidence. The UI shows this as a chat reply; user can approve or reject.
    - If **Auto Mode** (UI requested via `ChatSession.spec.preferAuto == true` and policy thresholds pass): Operator applies patches automatically (server-side apply / Helm), monitors rollout, and rolls back on failure.
6. **Stabilization & verification**
    - Operator waits for rollout/health checks and watches metrics to confirm stabilization (or triggers rollback + alert).
7. **Charts & Git commit**
    - When stabilized, Operator generates/updates artifacts:
        - Helm chart or kustomize overlay reflecting actual applied state.
        - Grafana dashboard JSON or generated chart images (before/after metrics).
    - The Operator’s Git Agent commits the artifacts to the configured repo (branch `smooth/<chat-id>` → create PR or push depending on policy).
8. **UI update & audit**
    - Operator updates `SmoothAction.status` with `applied: true/false`, `gitCommit`/PR URL, and final summary.
    - UI displays the final reply (diff, PR link, dashboard link) and stores the conversation/audit trail.

---

## 3 — Why letting the UI apply CRDs is good

- **Single declarative intent point:** The UI becomes the canonical intent object inside Kubernetes (everything is visible via `kubectl get chatsessions`).
- **RBAC & audit:** Applying CRDs uses cluster auth (service account / user token) so audit logs reflect who initiated the action.
- **Network locality:** Charts & metadata live as cluster objects (no external ephemeral storage required).
- **Resilience:** If UI goes down, the CRD still exists — operator continues processing when it reconnects.

---

## 4 — CRD examples (reflecting UI-applied intent)

### ChatSession (UI applies this)

```yaml
apiVersion: smooth.k8s.io/v1
kind: ChatSession
metadata:
  name: chat-2025-10-28-001
spec:
  user: "captain@example.com"
  targetNamespace: "payments"
  prompt: "Deploy python api on 8080, send logs to Loki, auto-mode on"
  charts:
    - name: "traffic-forecast"
      contentRef: "s3://..."   # or base64 blob inline
  metadata:
    gitRepo: "git@github.com:acme/apps.git"
    gitPath: "payments/api"
  preferAuto: true
  createdByUI: true     # explicit flag to show UI applied it
status: {}

```

### SmoothAction (Operator writes this back)

```yaml
apiVersion: smooth.k8s.io/v1
kind: SmoothAction
metadata:
  name: action-chat-2025-10-28-001
spec:
  chatRef: chat-2025-10-28-001
  mode: "auto" # or "suggest"
  inferredNeeds:
    - type: HPA
      reason: "CPU > 70% in 5m window"
    - type: Service(type=LoadBalancer)
      reason: "external access required"
  generatedArtifacts:
    - type: helm-chart
      path: charts/payments-api
status:
  state: "Applied"
  gitCommit: "abcd123"
  prURL: "<https://github.com/acme/apps/pull/42>"
  appliedAt: "2025-10-28T10:45:00Z"

```

---

## 5 — Operator watch behaviour (practical details)

- **Watch triggers**:
    - New/updated `ChatSession` CRDs (UI applied).
    - Changes to Deployments/Pods/Services in `targetNamespace` (so operator "waits for deployments" and reacts).
- **Collection window**:
    - For accuracy, the operator collects a short time-window of metrics (configurable, e.g., last 2–10 minutes) and recent events.
- **Image/Chart processing**:
    - If the UI uploads images, the operator can run a lightweight image-to-text/feature extractor (or send to LLM with descriptive alt text) before inference.
- **LLM Prompting**:
    - The operator uses a structured schema for inputs and expects structured JSON back (so the planner can deterministically translate to k8s manifests).
- **Time/Timeouts**:
    - The operator will wait for deployments to appear and stabilize (configurable timeouts). If nothing appears within the timeout, it returns a helpful message to the UI.

---

## 6 — Auto mode + safety (concise)

- UI sets `preferAuto:true` in the `ChatSession` if the user wants automation.
- Operator only **auto-applies** if:
    - Policies (OPA/Gatekeeper) pass.
    - Confidence & risk thresholds meet configured limits.
    - Dry-run succeeded and readiness checks pass in the stabilization window.
- If any step fails, operator **rolls back** and writes failure details to `SmoothAction.status` and the UI.

---

## 7 — Audit & GitOps behavior

- Every generated manifest and every LLM prompt/response is stored (encrypted) and linked to the `ChatSession`/`SmoothAction` for audit.
- Operator commits final artifacts to Git and includes:
    - `SMOOTH.md` with LLM rationale, diffs, and who/what triggered the change.
    - Optional CI job trigger for validation (test/helm lint).
- Configurable commit mode: `pr` (default) or `direct`.

---

## 8 — Sequence ASCII (compact)

```
[User UI] --applies--> ChatSession CRD ----------------> [K8s API]
      |                                                   |
      |<-- shows Suggests / PR links from SmoothAction ---|
      |
[User UI] shows conversation & chart upload
              |
         [Smooth Operator]
              |
 Collect: Deployments + Metrics + Chart blobs
              |
      LLM Adapter -> returns JSON plan
              |
  Planner -> Policy (OPA) -> Executor (apply/dry-run)
              |
 If applied -> wait stabilize -> generate charts -> Git Agent -> commit/pr
              |
 Update SmoothAction.status -> UI shows results

```

---

---

---

### PRD>>

# PRD — Smooth Operator (Conversational GitOps Operator for Kubernetes)

**One‑liner:** A Kubernetes‑native conversational operator. The Chat UI generates and applies Chat CRDs (prompt + metadata + charts). The **Smooth Operator** watches those CRDs and deployments, uses an LLM to infer what the project needs, replies with suggested diffs or (in auto mode) safely applies changes, then generates/updates Helm/Kustomize charts and commits to Git (GitOps).

---

## 0. Document Control

- **Owner:** Platform/ML Infra (Product + Eng)
- **Version:** v1.0 (draft)
- **Last Updated:** 2025‑10‑28
- **Status:** For review

---

## 1. Problem & Goals

### 1.1 Problem

Operating workloads on Kubernetes requires high YAML literacy and many repetitive decisions (exposure, autoscaling, probes, persistence, security, observability). This slows delivery, increases mistakes, and causes config drift between cluster and Git.

### 1.2 Goals

- Enable **natural‑language** intent to drive **safe, policy‑validated** cluster changes.
- Reduce repetitive YAML authoring by **LLM‑assisted inference** of missing/optimal resources.
- Provide **two modes**: Suggest (human‑in‑loop) and Auto (policy‑gated autonomous fixes).
- Ensure **GitOps** is the source of truth by generating/updating charts and committing to Git.
- Maintain **full auditability** (prompts, diffs, artifacts, approvals, rollbacks).

### 1.3 Non‑Goals

- Replacing GitOps engines (ArgoCD/Flux) — we integrate.
- Full app lifecycle (backlog, release orchestration) — out of scope.
- Cost optimization beyond basic rightsizing hints in v1.

### 1.4 Success Metrics (KPIs)

- **TTV (time‑to‑valid YAML):** ↓ 70% (median) vs. baseline.
- **Human changes auto‑generated:** ≥ 60% of new Service/HPA/Ingress/Probe added by Smooth.
- **Error rate:** < 2% failed rollouts attributable to Smooth changes.
- **Approval latency (Suggest mode):** P50 < 5 min.
- **Git drift incidents:** ↓ 80% vs. baseline.

---

## 2. Users & Use Cases

### 2.1 Personas

- **Dev**: wants "make my API available and autoscaled" without learning all k8s knobs.
- **SRE/Platform**: wants standards enforced (probes, resource policies, TLS, RBAC) with audit.
- **Tech Lead**: wants Git‑centric changes and quick reviews via PRs.

### 2.2 Primary Use Cases

1. **Expose a service**: User asks to expose app externally; Smooth proposes/creates Service LB/Ingress + certs.
2. **Autoscale**: User describes traffic; Smooth adds HPA with sane thresholds.
3. **Observability baseline**: Ensure logs to Loki, metrics scraped, dashboards scaffolded.
4. **Safety hardening**: Add probes, resource requests/limits, PodSecurity, and NetworkPolicy templates.
5. **Git chart creation**: After fixes, generate a Helm chart/kustomization and commit to repo.

---

## 3. Requirements

### 3.1 Functional Requirements (FR)

**FR‑1 Chat CRD ingestion**

- The **Chat UI** creates and applies `ChatSession` CRDs containing: prompt, target namespace, metadata (git repo/path), charts (links or blobs), and `preferAuto` flag.
- **Acceptance:** `kubectl get chatsessions` shows objects with supplied fields.

**FR‑2 Operator watches deployments and sessions**

- Smooth Operator watches `ChatSession` and k8s objects (Deployments/Pods/Services/Ingress) in target namespaces.
- **Acceptance:** Creating/modifying a Deployment triggers a new inference cycle if linked to an active ChatSession.

**FR‑3 Context collection**

- Collect Deployment specs, recent Events, Pod status, related Services/Ingress, and metrics (Prometheus). Optional: logs (Loki). Handle missing sources gracefully.
- **Acceptance:** Operator status logs show collected context or explicit “not available” markers.

**FR‑4 LLM inference (OpenAI adapter)**

- Build a **structured prompt** and call the LLM; expect **structured JSON** containing inferred needs, manifests/patches (or Helm values diffs), and rationale + confidence/risk.
- **Acceptance:** JSON parsed and validated against schema; errors reported without side effects.

**FR‑5 Suggest vs Auto mode**

- **Suggest (default):** produce `SmoothAction` with diffs + rationale; do not apply until approved (via UI or CRD field `approved: true`).
- **Auto:** if `preferAuto: true` **and** policy passes, apply changes immediately with dry‑run + server‑side apply.
- **Acceptance:** Mode honored; policy‑violating patches are blocked with reasons.

**FR‑6 Policy & safety**

- Integrate **OPA/Gatekeeper** or internal rules before apply. Enforce: image registries, namespace boundaries, resource limits, PodSecurity, NetworkPolicy presence, TLS rules, etc.
- **Acceptance:** Disallowed changes rejected with actionable error messages.

**FR‑7 Rollout health & rollback**

- Watch rollout and readiness. On failure/timeout, automatically rollback to previous healthy state; record cause in `SmoothAction.status`.
- **Acceptance:** Simulated failing rollout reverts; status includes reason and links to events/logs.

**FR‑8 GitOps artifact generation**

- Generate/update a **Helm chart** (or kustomize overlay) that mirrors applied state. Optionally generate Grafana dashboards and a `SMOOTH.md` rationale.
- **Acceptance:** Repo shows branch with chart changes; `helm lint` passes.

**FR‑9 Git commit/PR**

- Create branch `smooth/<chat-id>`, commit artifacts, and open PR (default). Direct commit is policy‑gated.
- **Acceptance:** PR created with summary, diff, labels, and reviewers per config.

**FR‑10 UI feedback loop**

- Operator posts back via `SmoothAction` CRD (status/diffs/links). UI renders message stream with buttons (Approve/Reject/Retry/Disable Auto).
- **Acceptance:** UI shows suggestions, progress, outcomes, PR links.

**FR‑11 Audit & retention**

- Store (encrypted): prompts, LLM responses, generated manifests, approvals, applied diffs, rollbacks, commits. Retention configurable.
- **Acceptance:** Retrieval API/CLI returns audit records scoped by ChatSession.

### 3.2 Non‑Functional Requirements (NFR)

- **Reliability:** No single operator crash should lose intent; CRDs are durable. Leader election on HA.
- **Performance:** P50 LLM turnaround < 10s (cached/short prompts); P95 < 30s. Apply + stabilization P50 < 3m for typical Deployments.
- **Scalability:** ≥ 200 concurrent active ChatSessions and 5,000 watched objects per cluster.
- **Security:** Least‑privilege RBAC; Git creds in Vault/SealedSecrets; outbound egress rules to LLM endpoint; transport TLS.
- **Compliance:** Audit logs immutable; data minimization (no secrets in prompts), PII redaction in logs.
- **Cost:** Budget guardrails on LLM calls (rate limit; cost per session cap; batch/token reuse).

---

## 4. System Overview

### 4.1 Components

- **Chat UI (external):** Authenticates to cluster, creates `ChatSession` CRDs. Renders `SmoothAction` results and PR links.
- **Smooth Operator:** Kubebuilder/Operator‑SDK controller.
    - Collector → LLM Adapter → Planner → Policy → Executor → Git Agent → Notifier.
- **Observability Stack:** Prometheus/Grafana/Loki/Tempo (optional but recommended).
- **Git Provider:** GitHub/GitLab/Bitbucket (via SSH key or app).
- **GitOps Engine:** ArgoCD/Flux (optional; we do not replace).

### 4.2 Sequence (end‑to‑end)

```
User → Chat UI → (applies) ChatSession CRD → K8s API
                                    ↓
                            Smooth Operator (watch)
 Collector → LLM Adapter → Planner → Policy → (Suggest|Auto)
     (if Auto) Executor → rollout/health → rollback if fail
 Post‑success: Git Agent (chart gen/commit/PR) → Notifier → UI

```

---

## 5. Data Model (CRDs)

> Note: OpenAPI schemas will be delivered with the implementation package.
> 

### 5.1 `ChatSession` (UI‑created)

```yaml
apiVersion: smooth.k8s.io/v1
kind: ChatSession
metadata:
  name: chat-<ts>-<rand>
spec:
  user: string                  # requester identity/email
  targetNamespace: string
  prompt: string                # natural language
  charts:                       # optional context
    - name: string
      url: string               # or
      blobBase64: string        # inline image/text if desired
  metadata:
    gitRepo: string             # SSH/HTTPS URL
    gitPath: string             # subdir path for app/chart
    labels:                     # arbitrary labels
      key: value
  preferAuto: bool              # default false
  createdByUI: bool             # true
status:
  state: Pending|Processing|Blocked|Completed|Failed
  reason: string
  lastUpdated: string (RFC3339)

```

### 5.2 `SmoothAction` (Operator‑created)

```yaml
apiVersion: smooth.k8s.io/v1
kind: SmoothAction
metadata:
  name: action-<chatName>
spec:
  chatRef: string
  mode: suggest|auto
  inferredNeeds:               # condensed plan
    - type: string             # e.g., HPA, Service-LB, Ingress, PVC, Probe, NP, PSP
      reason: string
      priority: low|med|high
  patches:                     # suggested manifests or values diffs
    - kind: string
      yaml: string             # kubernetes yaml
  generatedArtifacts:
    - type: helm-chart|kustomize|grafana-dashboard
      path: string             # repo path after commit
  approval:                    # for suggest mode
    required: bool
    approvedBy: string|null
status:
  state: Proposed|Applied|RolledBack|Declined|Error
  git:
    commit: string
    branch: string
    prURL: string
  appliedAt: string
  errors: []string

```

---

## 6. LLM Contract

### 6.1 Prompt Template (outline)

- **System:** “You are Smooth Planner. Output valid JSON per schema. Never invent secrets. Prefer minimal, safe manifests. Justify each change.”
- **Context:**
    - `user_prompt`
    - `cluster_context`: deployments/services/ingress YAML snippets
    - `metrics_snapshot`: CPU/mem/RPS/errors (5–10 min window)
    - `policies_summary`: constraints (images, namespaces, resource ceilings)
    - `charts_descriptions`: extracted from attached charts/blobs
- **Instruction:** “Return `inferredNeeds`, `patches`, `confidence` [0–1], `risk` [low|med|high], `explanation`.”

### 6.2 Expected JSON Schema (high level)

```json
{
  "inferredNeeds": [
    {"type":"HPA","reason":"...","priority":"high",
     "spec":{"minReplicas":2,"maxReplicas":10,"metrics":["cpu"]}}
  ],
  "patches": [
    {"kind":"HorizontalPodAutoscaler","yaml":"..."},
    {"kind":"Service","yaml":"..."}
  ],
  "confidence": 0.0,
  "risk": "low|med|high",
  "explanation": "string"
}

```

### 6.3 Guardrails

- Strict JSON parsing; reject if missing required keys.
- Redact/forbid secrets in prompts/responses.
- Size caps and truncation strategies; deterministic post‑processing.

---

## 7. Policy & Safety

- **OPA/Gatekeeper** policies for:
    - Allowed registries, namespaces, node selectors/taints.
    - Resource limits/requests; PodSecurity levels.
    - Mandatory probes; TLS on Ingress; disallow hostPath.
- **Risk gating:** auto mode only if `risk ∈ {low,med}` and `confidence ≥ threshold` (default 0.7).
- **Dry‑run first:** server‑side apply dry‑run + `helm template`/`helm lint`.
- **Health checks:** readiness gates, rollout watchers; rollback on threshold.

---

## 8. GitOps Integration

### 8.1 Artifact Generation

- **Helm chart** skeleton (templates + values.yaml) or **kustomize** overlay.
- Optional Grafana dashboards (JSON) and `SMOOTH.md` rationale file with:
    - Summary of changes
    - Before/after
    - Policy outcomes
    - Links to ChatSession/SmoothAction

### 8.2 Commit/PR Flow

1. Create branch `smooth/<chat-id>`.
2. Write artifacts under `gitPath`.
3. Commit with conventional message: `feat(smooth): expose + autoscale <service>`.
4. Open PR to default branch with labels: `bot`, `smooth-operator`, `autogenerated`.
5. Optional reviewers from config.

### 8.3 Repo Layout (example)

```
apps/
  payments/
    charts/payments-api/
      Chart.yaml
      templates/
      values.yaml
    dashboards/
      payments-overview.json
    SMOOTH.md

```

---

## 9. UI Requirements

- Create/apply `ChatSession` CRDs using cluster auth (kubeconfig or service token).
- Render `SmoothAction` updates as chat messages with:
    - Diff previews (unified view), risk/confidence badges.
    - Approve/Reject buttons (writes approval back to CRD).
    - Links to PR/commit and dashboard.
- Allow user to set `preferAuto` default and per‑session.
- Provide session history and audit export (JSON/MD/PDF).

---

## 10. Observability & Ops

- **Metrics (Prometheus):**
    - `smooth_inference_requests_total`
    - `smooth_inference_latency_seconds`
    - `smooth_actions_applied_total`
    - `smooth_rollbacks_total`
    - `smooth_policy_blocks_total`
- **Tracing:** OpenTelemetry spans across Collector → LLM → Planner → Policy → Executor → Git.
- **Logging:** Structured JSON with correlation IDs (chat ID).
- **Runbooks:**
    - LLM outage → degrade to rule‑based suggestions; pause Auto by feature flag.
    - Git failure → retry with backoff; queue artifacts; alert on >15m backlog.

---

## 11. Security & Privacy

- **RBAC:** Dedicated `smooth-operator` SA; only verbs required (get/list/watch/patch/apply) in target namespaces; no cluster‑admin.
- **Secrets:** Git/LLM creds in Vault/SealedSecrets; never send secrets to LLM; prompt scrubbing.
- **Network:** Egress allowlist to LLM and Git hosts; TLS enforced; mTLS optional.
- **Audit:** Immutable storage; access logged; retention per policy.

---

## 12. Performance & Capacity Targets

- **Cold start of operator:** < 10s.
- **P50 inference end‑to‑end:** < 10s; **P95** < 30s.
- **Concurrent sessions:** 200; queue + fair scheduling.
- **Backoff:** Exponential on LLM 429/5xx and Git 5xx.

---

## 13. Feature Flags & Config

- `autoMode.enabled` (bool, default false)
- `llm.provider` (openai|azure|…)
- `llm.model`, `llm.maxTokens`, `llm.temperature`
- `policy.requirements` (list of rules)
- `git.commitMode` (pr|direct), `git.reviewers` (list)
- `observability.integrations` (prometheus|grafana|loki)

---

## 14. Rollout Plan

- **Phase 0 (Dev):** Single‑namespace sandbox; Suggest mode only; Git to test repo.
- **Phase 1 (Pilot):** 2–3 teams; Auto allowed for low‑risk classes (Service/HPA/Probes).
- **Phase 2 (Org‑wide):** Multi‑namespace; policy hardened; SLOs tracked; CI checks on PRs.

---

## 15. Testing & Acceptance

- **Unit:** Planner, policy gates, CRD status transitions, diff renderers.
- **Integration (kind/k3d):** Deploy failing app; verify Smooth proposes probes/HPA/Service; approve → healthy rollout; Git PR present.
- **Chaos:** Kill operator pod mid‑apply → ensure idempotency; reprocess on restart.
- **Security:** Attempt disallowed image/hostPath → policy blocks with clear message.
- **Load:** 200 parallel ChatSessions; LLM rate limits handled; queue drains < 10m.

---

## 16. Risks & Mitigations

- **Hallucinated patches** → strict schema + policy + dry‑run; Suggest default.
- **Drift between Git and cluster** → always commit applied state; optionally let Argo/Flux reconcile.
- **Secret leakage** → scrub prompts; deny secret paths; static analyzers on outputs.
- **Over‑automation** → feature flag, per‑namespace allowlist, risk thresholding.

---

## 17. Open Questions

1. Do we want a rule‑based fallback (no LLM) for common patterns when LLM is offline?
2. How do we map deployments to the correct `gitPath` when multiple apps share a namespace?
3. Should we support direct Slack/Teams command ingress in v1 or post‑v1?

---

## 18. Appendix

### 18.1 Example Interaction

**Prompt:** “Deploy python API on 8080, logs to Loki, scale on CPU. Auto on.”

1. UI applies `ChatSession` (preferAuto=true). 2) Operator collects: Deployment, no Service, no HPA, Loki present. 3) LLM: add Service(LB), HPA(cpu 70%), probes. 4) Policy OK. 5) Operator applies; watches rollout; success. 6) Generates Helm chart + dashboard; commits branch `smooth/chat‑...`; PR opened. 7) UI shows success + PR link.

### 18.2 ASCII: Detailed Flow

```
[Chat UI]
  └─> create ChatSession (CRD)
        └─> [K8s API]
              └─> [Smooth Operator]
                    ├─ Collector (k8s objs, metrics, charts)
                    ├─ LLM Adapter (JSON plan)
                    ├─ Planner (diff/manifests)
                    ├─ Policy (OPA/Gatekeeper)
                    ├─ (Suggest|Auto)
                    │     └─ Executor (apply/rollback)
                    ├─ Git Agent (chart gen + commit + PR)
                    └─ Notifier (SmoothAction status)

```

### 18.3 Sample Policies (pseudo‑rego)

```
package smooth.policies

deny[msg] {
  input.kind == "Deployment"
  not input.spec.template.spec.securityContext.runAsNonRoot
  msg := "runAsNonRoot required"
}

deny[msg] {
  input.kind == "Service"
  input.spec.type == "LoadBalancer"
  not input.metadata.annotations["service.beta.kubernetes.io/aws-load-balancer-internal"]
  msg := "LB must be internal unless approved"
}

```

### 18.4 Minimal Helm Chart Skeleton

```
charts/<app>/
  Chart.yaml
  values.yaml
  templates/
    deployment.yaml
    service.yaml
    hpa.yaml
    ingress.yaml
    configmap.yaml

```

### 18.5 One‑Sentence Task Backlog (for kickoff)

- Scaffold CRDs (`ChatSession`, `SmoothAction`) with OpenAPI schemas.
- Implement operator skeleton with watches and reconciliation loop.
- Build Collector: k8s discovery + Prometheus client + optional Loki.
- Implement LLM Adapter with JSON schema validation and retries.
- Implement Planner (manifest synthesis + helm/kustomize generators).
- Wire Policy checks (OPA/Gatekeeper) pre‑apply.
- Implement Executor (dry‑run, server‑side apply, rollout watcher, rollback).
- Implement Git Agent (branch/commit/PR; repo auth via Vault/SealedSecrets).
- Implement Notifier (update SmoothAction; diff summaries; PR links).
- Build Chat UI to create/apply ChatSession; render SmoothAction; approve buttons.
- Add metrics/tracing/logging; dashboards for operator health.
- Add feature flags; namespace allowlist; risk thresholds.
- Write runbooks; load/chaos tests; security review.

```

```