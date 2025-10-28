/*
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
*/

package collector

import (
	"context"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// ContextKey type for context values
type ContextKey string

const (
	// ChatSessionIDKey is the context key for chat session ID (correlation ID)
	ChatSessionIDKey ContextKey = "chatSessionID"
	// NamespaceKey is the context key for target namespace
	NamespaceKey ContextKey = "targetNamespace"
)

// WithChatSessionID adds a chat session ID to the context for correlation
func WithChatSessionID(ctx context.Context, chatSessionID string) context.Context {
	// Add to context values
	ctx = context.WithValue(ctx, ChatSessionIDKey, chatSessionID)

	// Add to logger
	logger := log.FromContext(ctx)
	logger = logger.WithValues("chatSessionID", chatSessionID)
	ctx = log.IntoContext(ctx, logger)

	return ctx
}

// WithNamespace adds a namespace to the context for correlation
func WithNamespace(ctx context.Context, namespace string) context.Context {
	// Add to context values
	ctx = context.WithValue(ctx, NamespaceKey, namespace)

	// Add to logger
	logger := log.FromContext(ctx)
	logger = logger.WithValues("targetNamespace", namespace)
	ctx = log.IntoContext(ctx, logger)

	return ctx
}

// GetChatSessionID retrieves the chat session ID from context
func GetChatSessionID(ctx context.Context) string {
	if id, ok := ctx.Value(ChatSessionIDKey).(string); ok {
		return id
	}
	return ""
}

// GetNamespace retrieves the namespace from context
func GetNamespace(ctx context.Context) string {
	if ns, ok := ctx.Value(NamespaceKey).(string); ok {
		return ns
	}
	return ""
}

// EnrichLogger adds correlation fields to a logger
func EnrichLogger(logger logr.Logger, chatSessionID, namespace string) logr.Logger {
	return logger.WithValues(
		"chatSessionID", chatSessionID,
		"targetNamespace", namespace,
	)
}
