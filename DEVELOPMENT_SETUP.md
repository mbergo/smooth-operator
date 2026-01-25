# Smooth Operator - Local Development Setup

## Overview

This guide explains how to run the Smooth Operator locally while connected to a remote Kubernetes cluster at 192.168.0.3.

## Setup Complete ✅

The following has been configured for you:

1. **Kubernetes Cluster Access**: Connected to cluster at 192.168.0.3
   - Kubeconfig: `~/.kube/config` 
   - Backup: `kubeconfig-192.168.0.3.yaml`
   - TLS verification disabled due to certificate constraints

2. **CRDs Installed**: Custom Resource Definitions are deployed to the cluster
   - `chatsessions.smoothoperator.io`
   - `smoothactions.smoothoperator.io`

3. **Environment Configuration**: 
   - `.env.development` - Environment variables (you need to add your OpenAI API key)
   - `run-dev.sh` - Development runner script

## Quick Start

### 1. Set Your OpenAI API Key

Edit `.env.development` and replace `your-openai-api-key-here` with your actual OpenAI API key:

```bash
OPENAI_API_KEY=sk-your-actual-api-key-here
```

### 2. Run the Operator Locally

```bash
./run-dev.sh
```

This will:
- Load environment variables
- Connect to the remote cluster at 192.168.0.3
- Start the operator in development mode with debug logging

### 3. Test with a ChatSession

In another terminal, create a test ChatSession:

```bash
kubectl apply -f config/samples/smooth_v1_chatsession.yaml
```

Watch the operator logs in your first terminal to see it process the request.

## How It Works

The Smooth Operator watches for `ChatSession` resources and:

1. **Collects Context**: Gathers information about resources in the target namespace
2. **Consults LLM**: Sends the context and prompt to OpenAI to generate an infrastructure plan
3. **Creates Plan**: Generates Kubernetes manifests based on the LLM's response
4. **Applies Changes**: Either suggests changes (preferAuto: false) or applies them automatically (preferAuto: true)

## Example Use Case

Instead of writing YAML files manually, you can create a ChatSession like:

```yaml
apiVersion: smoothoperator.io/v1
kind: ChatSession
metadata:
  name: deploy-my-app
spec:
  user: "developer@company.com"
  targetNamespace: "production"
  prompt: "I have a Node.js app at github.com/myorg/myapp. It needs MongoDB and Redis. Set up the full stack with proper health checks and monitoring."
  preferAuto: false
```

The operator will:
- Analyze your repository
- Understand the requirements
- Generate appropriate Kubernetes manifests
- Create a plan for review (or apply automatically if preferAuto: true)

## Monitoring

Check operator status:
```bash
kubectl get chatsessions -w
kubectl describe chatsession <name>
```

View generated actions:
```bash
kubectl get smoothactions
```

## Troubleshooting

1. **Operator not starting**: Check that your OpenAI API key is set correctly
2. **Connection issues**: Verify `kubectl cluster-info` works
3. **CRD errors**: Run `make install` to reinstall CRDs

## Next Steps

- Modify the operator code in `internal/controller/`
- Run `make generate` after API changes
- Test different prompts and scenarios
- Enable Prometheus/Loki integration for better observability