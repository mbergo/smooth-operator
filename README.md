# Smooth Operator

**A Kubernetes-native conversational operator that uses LLMs to infer, suggest, and apply infrastructure changes based on natural language prompts.**

## Overview

Smooth Operator is a GitOps-aware Kubernetes operator that bridges the gap between natural language intent and Kubernetes resources. It watches `ChatSession` CRDs (created by a Chat UI), analyzes cluster state and metrics, consults an LLM (like OpenAI) to determine what's needed, and then either suggests changes (with human approval) or applies them automatically (with policy gates).

### Key Features

- **Natural Language to Kubernetes**: Describe what you want in plain English, get valid K8s manifests
- **Two Modes**: 
  - **Suggest Mode** (default): Propose changes, wait for approval
  - **Auto Mode**: Apply changes automatically if policies pass
- **Policy-Gated**: Integrates with OPA/Gatekeeper for safety
- **GitOps-First**: Generates Helm charts and commits to Git
- **Full Audit Trail**: Every prompt, response, and action is logged
- **Observability-Aware**: Collects metrics (Prometheus), logs (Loki), and cluster state

### Architecture

```
User → Chat UI → ChatSession CRD → K8s API
                           ↓
                   Smooth Operator
                           ↓
   Collector → LLM → Planner → Policy → Executor → Git Agent
                           ↓
                   SmoothAction CRD (results)
```

## Description

Operating Kubernetes workloads requires deep YAML expertise and repetitive decision-making (exposure, autoscaling, probes, persistence, security, observability). Smooth Operator automates these decisions using LLMs while maintaining safety through policy enforcement, dry-runs, and human-in-the-loop approval flows.

**Use Cases:**
- Expose a service externally with load balancer + TLS
- Add autoscaling (HPA) based on CPU/memory metrics
- Ensure health probes, resource limits, and security policies
- Generate and commit Helm charts to Git
- Create Grafana dashboards for new services

### Input Requirements

The operator processes `ChatSession` CRDs that contain a git repository reference in their metadata. **The git repository can be any application git repository that the operator is able to fetch** (via SSH or HTTPS). The operator uses this repository to:
- Understand the application context
- Commit generated Helm charts and Kubernetes manifests
- Create pull requests with infrastructure changes

The operator requires appropriate credentials (SSH keys or access tokens) to be configured for fetching from and pushing to the specified git repository.

## Getting Started

### Prerequisites
- **Go** version v1.21.0+ (tested with v1.25.1)
- **Docker** version 17.03+
- **kubectl** version v1.11.3+
- **Kubebuilder** v4.9.0+ (installed automatically by setup scripts)
- Access to a Kubernetes v1.11.3+ cluster (or use **kind**/**k3d** for local development)

### Quick Start (Local Development)

1. **Clone the repository**:
   ```bash
   git clone https://github.com/mbergo/smooth-operator.git
   cd smooth-operator
   ```

2. **Create a local Kubernetes cluster** (using kind):
   ```bash
   kind create cluster --name smooth-operator-dev
   ```

3. **Install CRDs**:
   ```bash
   make install
   ```

4. **Run the operator locally**:
   ```bash
   make run
   ```

5. **In another terminal, create a sample ChatSession**:
   ```bash
   kubectl apply -f config/samples/smooth_v1_chatsession.yaml
   ```

6. **Watch the operator process the session**:
   ```bash
   kubectl get chatsessions -w
   kubectl describe chatsession chatsession-sample
   ```

For detailed development instructions, see [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md).

### To Deploy on the cluster
**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/smooth-operator:tag
```

**NOTE:** This image ought to be published in the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don’t work.

**Install the CRDs into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/smooth-operator:tag
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
privileges or be logged in as admin.

**Create instances of your solution**
You can apply the samples (examples) from the config/sample:

```sh
kubectl apply -k config/samples/
```

>**NOTE**: Ensure that the samples has default values to test it out.

### To Uninstall
**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/
```

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

## Project Distribution

Following the options to release and provide this solution to the users.

### By providing a bundle with all YAML files

1. Build the installer for the image built and published in the registry:

```sh
make build-installer IMG=<some-registry>/smooth-operator:tag
```

**NOTE:** The makefile target mentioned above generates an 'install.yaml'
file in the dist directory. This file contains all the resources built
with Kustomize, which are necessary to install this project without its
dependencies.

2. Using the installer

Users can just run 'kubectl apply -f <URL for YAML BUNDLE>' to install
the project, i.e.:

```sh
kubectl apply -f https://raw.githubusercontent.com/<org>/smooth-operator/<tag or branch>/dist/install.yaml
```

### By providing a Helm Chart

1. Build the chart using the optional helm plugin

```sh
kubebuilder edit --plugins=helm/v1-alpha
```

2. See that a chart was generated under 'dist/chart', and users
can obtain this solution from there.

**NOTE:** If you change the project, you need to update the Helm Chart
using the same command above to sync the latest changes. Furthermore,
if you create webhooks, you need to use the above command with
the '--force' flag and manually ensure that any custom configuration
previously added to 'dist/chart/values.yaml' or 'dist/chart/manager/manager.yaml'
is manually re-applied afterwards.

## Contributing
// TODO(user): Add detailed information on how you would like others to contribute to this project

**NOTE:** Run `make help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## License

Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

