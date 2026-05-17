# smooth-operator Helm chart

Installs the Smooth Operator controller — a Kubernetes-native conversational
operator that watches `ChatSession` CRDs and produces `SmoothAction` results.

## TL;DR

```bash
helm repo add smooth-operator https://mbergo.github.io/smooth-operator
helm install smooth-operator smooth-operator/smooth-operator \
  --namespace smooth-operator-system --create-namespace \
  --set operator.llmApiKeySecret=smooth-operator-llm \
  --set operator.gitTokenSecret=smooth-operator-git
```

Until the chart is published, install from the local checkout:

```bash
helm install smooth-operator ./dist/chart \
  --namespace smooth-operator-system --create-namespace
```

## Prerequisites

- Kubernetes 1.28+
- A Secret in the release namespace containing `LLM_API_KEY` (referenced by
  `operator.llmApiKeySecret`).
- Optional: a Secret containing `GITHUB_TOKEN`, `GITLAB_TOKEN`, and `LOKI_TOKEN`
  (referenced by `operator.gitTokenSecret`).

## Configuration

See `values.yaml` for the full set of tunables. Key fields:

| Key | Default | Purpose |
|-----|---------|---------|
| `image.repository` | `ghcr.io/mbergo/smooth-operator` | Operator image |
| `image.tag` | `""` (uses appVersion) | Override image tag |
| `replicaCount` | `1` | Controller replicas |
| `crds.install` | `true` | Install CRDs with the chart |
| `crds.keep` | `true` | Keep CRDs on uninstall |
| `rbac.create` | `true` | Install ClusterRole + bindings |
| `metrics.enabled` | `true` | Expose Prometheus metrics |
| `leaderElection.enabled` | `true` | Enable leader election |
| `operator.llmProvider` | `openai` | LLM backend |
| `operator.llmModel` | `gpt-4-turbo-preview` | Model name |
| `operator.llmApiKeySecret` | `""` | Secret with `LLM_API_KEY` |
| `operator.gitTokenSecret` | `""` | Secret with provider tokens |
| `operator.lokiURL` | `""` | Optional Loki base URL |
| `operator.prometheusURL` | `""` | Optional Prometheus base URL |
| `operator.autoMode` | `false` | Default auto-mode flag |

## Uninstall

```bash
helm uninstall smooth-operator -n smooth-operator-system
```

CRDs are retained by default (`crds.keep: true`). To remove them:

```bash
kubectl delete crd chatsessions.smooth.smooth.k8s.io smoothactions.smooth.smooth.k8s.io
```

## Source

https://github.com/mbergo/smooth-operator
