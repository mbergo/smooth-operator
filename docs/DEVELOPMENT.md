# Smooth Operator - Development Guide

## Overview

This guide covers setting up the development environment, building, testing, and running the Smooth Operator locally.

## Prerequisites

- **Go 1.21+** (tested with Go 1.25.1)
- **Docker** (for building container images)
- **kubectl** (for interacting with Kubernetes clusters)
- **kind** or **k3d** (for local Kubernetes cluster)
- **Kubebuilder 4.9.0+** (for operator development)

## Project Structure

```
smooth-operator/
├── api/v1/                      # CRD definitions (ChatSession, SmoothAction)
├── config/                      # Kubernetes manifests
│   ├── crd/bases/              # Generated CRD YAML
│   ├── rbac/                   # RBAC roles and bindings
│   ├── manager/                # Operator deployment
│   └── default/                # Kustomize base
├── internal/controller/         # Controller reconciliation logic
├── cmd/main.go                  # Operator entry point
├── docs/                        # Documentation
├── test/                        # E2E and integration tests
├── Makefile                     # Build and deployment targets
└── PRJ.md                       # Project requirements document (PRD)
```

## Quick Start

### 1. Install Dependencies

#### Install Kubebuilder

```bash
curl -L -o kubebuilder "https://go.kubebuilder.io/dl/latest/$(go env GOOS)/$(go env GOARCH)"
chmod +x kubebuilder
sudo mv kubebuilder /usr/local/bin/
```

#### Install kind (Kubernetes in Docker)

```bash
# For Linux
curl -Lo ./kind https://kind.sigs.k8s.io/dl/v0.20.0/kind-linux-amd64
chmod +x ./kind
sudo mv ./kind /usr/local/bin/kind

# For macOS
brew install kind
```

#### Install k3d (alternative to kind)

```bash
# For Linux/macOS
curl -s https://raw.githubusercontent.com/k3d-io/k3d/main/install.sh | bash
```

### 2. Create Local Kubernetes Cluster

#### Using kind:

```bash
kind create cluster --name smooth-operator-dev
```

#### Using k3d:

```bash
k3d cluster create smooth-operator-dev --api-port 6443 --servers 1 --agents 2
```

### 3. Build and Run Locally

#### Install CRDs

```bash
make install
```

This command installs the ChatSession and SmoothAction CRDs into your cluster.

#### Run the Operator Locally

```bash
make run
```

This runs the operator outside the cluster, connecting to your local kind/k3d cluster using your kubeconfig.

## Development Workflow

### Makefile Targets

| Target | Description |
|--------|-------------|
| `make manifests` | Generate CRD and RBAC manifests from Go code |
| `make generate` | Generate deep-copy functions for Go types |
| `make install` | Install CRDs into the cluster |
| `make uninstall` | Remove CRDs from the cluster |
| `make run` | Run the operator locally (outside cluster) |
| `make build` | Build the operator binary |
| `make docker-build` | Build the operator Docker image |
| `make docker-push` | Push the operator Docker image to registry |
| `make deploy` | Deploy the operator to the cluster |
| `make undeploy` | Remove the operator from the cluster |
| `make test` | Run unit tests |
| `make lint` | Run linters (golangci-lint) |

### Typical Development Cycle

1. **Modify CRD schemas** in `api/v1/*_types.go`
2. **Regenerate manifests**: `make manifests generate`
3. **Update CRDs in cluster**: `make install`
4. **Implement controller logic** in `internal/controller/*_controller.go`
5. **Run locally**: `make run`
6. **Test changes** by applying sample CRDs
7. **Run tests**: `make test`
8. **Commit changes**

### Testing the Operator

#### Create a Sample ChatSession

```bash
kubectl apply -f - <<EOF
apiVersion: smooth.smooth.k8s.io/v1
kind: ChatSession
metadata:
  name: test-session
  namespace: default
spec:
  user: "developer@example.com"
  targetNamespace: "default"
  prompt: "Deploy a simple nginx server and expose it"
  metadata:
    gitRepo: "git@github.com:example/repo.git"
    gitPath: "apps/nginx"
  preferAuto: false
  createdByUI: true
EOF
```

#### Watch the ChatSession Status

```bash
kubectl get chatsessions -w
```

#### View Operator Logs

```bash
# If running locally with make run:
# Logs appear in your terminal

# If deployed to cluster:
kubectl logs -n smooth-operator-system deployment/smooth-operator-controller-manager -f
```

#### Describe the ChatSession for Details

```bash
kubectl describe chatsession test-session
```

## Building for Production

### Build Container Image

```bash
# Build the image
make docker-build IMG=your-registry/smooth-operator:v0.1.0

# Push to registry
make docker-push IMG=your-registry/smooth-operator:v0.1.0
```

### Deploy to Cluster

```bash
# Deploy the operator
make deploy IMG=your-registry/smooth-operator:v0.1.0

# Verify deployment
kubectl get deployment -n smooth-operator-system
```

## Debugging

### Enable Verbose Logging

When running locally:

```bash
make run ARGS="--zap-log-level=debug --zap-devel=true"
```

### Debug in VS Code

Add to `.vscode/launch.json`:

```json
{
  "version": "0.2.0",
  "configurations": [
    {
      "name": "Debug Operator",
      "type": "go",
      "request": "launch",
      "mode": "auto",
      "program": "${workspaceFolder}/cmd/main.go",
      "env": {},
      "args": []
    }
  ]
}
```

### Common Issues

#### CRDs Not Found

```bash
# Reinstall CRDs
make install

# Or manually apply
kubectl apply -f config/crd/bases/
```

#### Permission Denied

Check RBAC roles:

```bash
kubectl describe clusterrole manager-role
```

#### Operator Not Starting

Check deployment status:

```bash
kubectl describe deployment -n smooth-operator-system smooth-operator-controller-manager
kubectl logs -n smooth-operator-system deployment/smooth-operator-controller-manager
```

## Testing Strategy

### Unit Tests

```bash
# Run all unit tests
make test

# Run specific test
go test -v ./internal/controller/... -run TestChatSessionReconciler
```

### Integration Tests

```bash
# Run integration tests (requires cluster)
make test-integration
```

### E2E Tests

```bash
# Run end-to-end tests
make test-e2e
```

## Clean Up

### Remove CRDs and Operator

```bash
# Undeploy operator
make undeploy

# Uninstall CRDs
make uninstall
```

### Delete Local Cluster

```bash
# kind
kind delete cluster --name smooth-operator-dev

# k3d
k3d cluster delete smooth-operator-dev
```

## Next Steps

### Phase 1: Context Collection (In Progress)

- [ ] Implement Kubernetes resource collector (Deployments, Services, Ingress)
- [ ] Add Prometheus metrics collection
- [ ] Add optional Loki log aggregation
- [ ] Implement graceful degradation for missing observability

### Phase 2: LLM Integration (Planned)

- [ ] Design LLM prompt template
- [ ] Implement OpenAI API adapter
- [ ] Add JSON schema validation for LLM responses
- [ ] Implement secret scrubbing and safety guardrails

### Phase 3: Policy & Planning (Planned)

- [ ] Integrate OPA for policy evaluation
- [ ] Implement manifest planner
- [ ] Add diff generation
- [ ] Create SmoothAction CRD population

## Resources

- **Kubebuilder Book**: https://book.kubebuilder.io/
- **Controller Runtime**: https://github.com/kubernetes-sigs/controller-runtime
- **Operator SDK**: https://sdk.operatorframework.io/
- **PRD Document**: [PRJ.md](../PRJ.md)

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines on contributing to this project.

## License

Apache License 2.0 - See [LICENSE](../LICENSE) for details.

