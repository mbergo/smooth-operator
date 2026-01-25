#!/bin/bash
# Development runner script for Smooth Operator

# Load environment variables
if [ -f .env.development ]; then
    echo "Loading environment variables from .env.development..."
    export $(cat .env.development | grep -v '^#' | grep -v '^$' | xargs)
else
    echo "Warning: .env.development file not found!"
    echo "Please copy .env.development.example and set your OpenAI API key"
    exit 1
fi

# Check if OpenAI API key is set
if [ -z "$OPENAI_API_KEY" ] || [ "$OPENAI_API_KEY" = "your-openai-api-key-here" ]; then
    echo "Error: OPENAI_API_KEY not set in .env.development"
    echo "Please set your OpenAI API key before running the operator"
    exit 1
fi

echo "Starting Smooth Operator in development mode..."
echo "Connected to Kubernetes cluster at: $(kubectl config current-context)"
echo ""

# Run the operator
make run