# Smooth Operator Chat UI

React-based chat interface for Smooth Operator - Conversational GitOps for Kubernetes.

## Features

- 💬 Natural language interface for Kubernetes operations
- 🔐 K8s authentication (kubeconfig)
- 📝 Real-time ChatSession CRD creation
- 👁️ Status watching and updates
- 🎨 Beautiful, modern UI with gradients
- ⚙️ Configurable (namespace, Git repo, auto mode)

## Quick Start

```bash
# Install dependencies
cd ui
npm install

# Run development server
npm run dev

# In another terminal, start kubectl proxy for K8s API access
kubectl proxy --port=8001

# Open browser
open http://localhost:3000
```

## Usage

1. Configure namespace and Git repository
2. Toggle auto mode if desired
3. Type your request: "Deploy my API with autoscaling"
4. Watch the operator work!
5. See results with PR links

## Build for Production

```bash
npm run build
# Output in dist/
```

## Architecture

```
React App (port 3000)
    ↓
kubectl proxy (port 8001)
    ↓
Kubernetes API
    ↓
ChatSession CRD created
    ↓
Smooth Operator processes
    ↓
SmoothAction CRD created
    ↓
UI displays results + PR link
```

## Technology

- React 18
- TypeScript
- Vite
- @kubernetes/client-node
- CSS3 with gradients

