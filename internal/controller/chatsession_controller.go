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

package controller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	smoothv1 "github.com/mbergo/smooth-operator/api/v1"
	"github.com/mbergo/smooth-operator/internal/collector"
	"github.com/mbergo/smooth-operator/internal/llm"
	"github.com/mbergo/smooth-operator/internal/planner"
)

// ChatSessionReconciler reconciles a ChatSession object
type ChatSessionReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	Aggregator  *collector.Aggregator
	LLMClient   *llm.Client
	Planner     *planner.Planner
	Reporter    *planner.Reporter
}

// +kubebuilder:rbac:groups=smooth.smooth.k8s.io,resources=chatsessions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=smooth.smooth.k8s.io,resources=chatsessions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=smooth.smooth.k8s.io,resources=chatsessions/finalizers,verbs=update
// +kubebuilder:rbac:groups=smooth.smooth.k8s.io,resources=smoothactions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// The ChatSession controller watches ChatSession CRDs and triggers the
// Smooth Operator pipeline: context collection, LLM inference, policy checks,
// and execution (suggest or auto mode).
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.22.1/pkg/reconcile
func (r *ChatSessionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the ChatSession instance
	chatSession := &smoothv1.ChatSession{}
	err := r.Get(ctx, req.NamespacedName, chatSession)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// ChatSession was deleted, nothing to do
			log.Info("ChatSession resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request
		log.Error(err, "Failed to get ChatSession")
		return ctrl.Result{}, err
	}

	// Log the received ChatSession
	log.Info("Reconciling ChatSession",
		"name", chatSession.Name,
		"namespace", chatSession.Namespace,
		"user", chatSession.Spec.User,
		"targetNamespace", chatSession.Spec.TargetNamespace,
		"prompt", chatSession.Spec.Prompt,
		"preferAuto", chatSession.Spec.PreferAuto,
	)

	// Initialize status if not set
	if chatSession.Status.State == "" {
		chatSession.Status.State = "Pending"
		chatSession.Status.Reason = "ChatSession received, waiting to begin processing"
		chatSession.Status.LastUpdated = metav1.Now().Format(time.RFC3339)

		// Set the initial condition
		meta.SetStatusCondition(&chatSession.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "Pending",
			Message:            "ChatSession is pending processing",
			LastTransitionTime: metav1.Now(),
		})

		if err := r.Status().Update(ctx, chatSession); err != nil {
			log.Error(err, "Failed to update ChatSession status to Pending")
			return ctrl.Result{}, err
		}

		// Requeue immediately to start processing
		return ctrl.Result{Requeue: true}, nil
	}

	// Check if already completed or failed
	if chatSession.Status.State == "Completed" || chatSession.Status.State == "Failed" {
		log.Info("ChatSession already in terminal state", "state", chatSession.Status.State)
		return ctrl.Result{}, nil
	}

	// Begin processing if in Pending state
	if chatSession.Status.State == "Pending" {
		chatSession.Status.State = "Processing"
		chatSession.Status.Reason = "Starting context collection and LLM inference"
		chatSession.Status.LastUpdated = metav1.Now().Format(time.RFC3339)

		meta.SetStatusCondition(&chatSession.Status.Conditions, metav1.Condition{
			Type:               "Processing",
			Status:             metav1.ConditionTrue,
			Reason:             "ContextCollection",
			Message:            "Collecting cluster context and metrics",
			LastTransitionTime: metav1.Now(),
		})

		if err := r.Status().Update(ctx, chatSession); err != nil {
			log.Error(err, "Failed to update ChatSession status to Processing")
			return ctrl.Result{}, err
		}

		// Requeue to continue processing
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// Stage 1: Context Collection & LLM Inference (Phase 1 + Phase 2)
	if chatSession.Status.State == "Processing" {
		log.Info("Collecting cluster context", "targetNamespace", chatSession.Spec.TargetNamespace)

		// Add correlation ID to context
		ctx = collector.WithChatSessionID(ctx, chatSession.Name)
		ctx = collector.WithNamespace(ctx, chatSession.Spec.TargetNamespace)

		// Collect and aggregate context
		aggregated, err := r.Aggregator.AggregateContext(ctx, chatSession.Spec.TargetNamespace, chatSession.Name)
		if err != nil {
			log.Error(err, "Failed to aggregate cluster context")
			chatSession.Status.State = "Failed"
			chatSession.Status.Reason = fmt.Sprintf("Context aggregation failed: %v", err)
			chatSession.Status.LastUpdated = metav1.Now().Format(time.RFC3339)

			meta.SetStatusCondition(&chatSession.Status.Conditions, metav1.Condition{
				Type:               "Failed",
				Status:             metav1.ConditionTrue,
				Reason:             "ContextCollectionFailed",
				Message:            fmt.Sprintf("Failed to aggregate cluster context: %v", err),
				LastTransitionTime: metav1.Now(),
			})

			if updateErr := r.Status().Update(ctx, chatSession); updateErr != nil {
				log.Error(updateErr, "Failed to update ChatSession status to Failed")
			}
			return ctrl.Result{}, err
		}

		log.Info("Context aggregated successfully",
			"deployments", aggregated.Summary.TotalDeployments,
			"services", aggregated.Summary.TotalServices,
			"collectionDuration", aggregated.CollectionDuration.String(),
		)

		// Stage 2: LLM Inference (Phase 2 implementation)
		if r.LLMClient != nil && r.LLMClient.IsEnabled() {
			log.Info("Calling LLM for plan generation")

			llmResponse, err := r.LLMClient.GeneratePlan(ctx, chatSession.Spec.Prompt, aggregated)
			if err != nil {
				log.Error(err, "LLM plan generation failed")
				chatSession.Status.State = "Failed"
				chatSession.Status.Reason = fmt.Sprintf("LLM inference failed: %v", err)
				chatSession.Status.LastUpdated = metav1.Now().Format(time.RFC3339)

				if updateErr := r.Status().Update(ctx, chatSession); updateErr != nil {
					log.Error(updateErr, "Failed to update status after LLM failure")
				}
				return ctrl.Result{}, err
			}

			log.Info("LLM plan generated",
				"inferredNeeds", len(llmResponse.InferredNeeds),
				"patches", len(llmResponse.Patches),
				"confidence", llmResponse.Confidence,
				"risk", llmResponse.Risk,
			)

			// Stage 3: Policy & Planning validation (Phase 3 implementation)
			log.Info("Validating plan with policies and risk assessment")

			executionPlan, err := r.Planner.CreatePlan(
				ctx,
				llmResponse,
				chatSession.Spec.TargetNamespace,
				chatSession.Spec.PreferAuto,
			)
			if err != nil {
				log.Error(err, "Failed to create execution plan")
				chatSession.Status.State = "Failed"
				chatSession.Status.Reason = fmt.Sprintf("Plan validation failed: %v", err)
				chatSession.Status.LastUpdated = metav1.Now().Format(time.RFC3339)

				if updateErr := r.Status().Update(ctx, chatSession); updateErr != nil {
					log.Error(updateErr, "Failed to update status after plan failure")
				}
				return ctrl.Result{}, err
			}

			// Log plan summary
			planSummary := r.Reporter.FormatPlanSummary(executionPlan)
			log.Info("Execution plan created",
				"manifests", len(executionPlan.Manifests),
				"policyViolations", len(executionPlan.PolicyResults.Violations),
				"overallRisk", executionPlan.RiskAssessment.OverallRisk,
				"recommendation", executionPlan.RiskAssessment.Recommendation,
			)
			log.V(1).Info("Plan summary", "summary", planSummary)

			// Check if plan can be auto-applied
			canAutoApply := r.Planner.ShouldAutoApply(executionPlan, chatSession.Spec.PreferAuto)
			log.Info("Auto-apply decision",
				"requested", chatSession.Spec.PreferAuto,
				"allowed", canAutoApply,
			)

			// Create SmoothAction CRD with validated plan
			err = r.createSmoothAction(ctx, chatSession, llmResponse, executionPlan)
			if err != nil {
				log.Error(err, "Failed to create SmoothAction")
				return ctrl.Result{}, err
			}

			// Update status to completed
			chatSession.Status.State = "Completed"
			chatSession.Status.Reason = fmt.Sprintf(
				"Plan generated with %d suggestions (confidence: %.0f%%, risk: %s)",
				len(llmResponse.InferredNeeds), llmResponse.Confidence*100, llmResponse.Risk,
			)
			chatSession.Status.LastUpdated = metav1.Now().Format(time.RFC3339)

			meta.SetStatusCondition(&chatSession.Status.Conditions, metav1.Condition{
				Type:               "Completed",
				Status:             metav1.ConditionTrue,
				Reason:             "LLMPlanGenerated",
				Message:            "Successfully generated improvement plan",
				LastTransitionTime: metav1.Now(),
			})

			if err := r.Status().Update(ctx, chatSession); err != nil {
				log.Error(err, "Failed to update ChatSession status to Completed")
				return ctrl.Result{}, err
			}

			log.Info("ChatSession completed successfully")
			return ctrl.Result{}, nil
		}

		// If LLM not enabled, just show what we collected
		chatSession.Status.State = "Completed"
		chatSession.Status.Reason = fmt.Sprintf(
			"Context collected (LLM disabled): %d deployments, %d services, %d issues detected",
			aggregated.Summary.TotalDeployments,
			aggregated.Summary.TotalServices,
			len(aggregated.Summary.DeploymentsWithoutService)+
				len(aggregated.Summary.DeploymentsWithoutProbes)+
				len(aggregated.Summary.HighCPUDeployments),
		)
		chatSession.Status.LastUpdated = metav1.Now().Format(time.RFC3339)

		if err := r.Status().Update(ctx, chatSession); err != nil {
			log.Error(err, "Failed to update ChatSession status")
			return ctrl.Result{}, err
		}

		log.Info("Context collection complete (LLM not enabled)")
	return ctrl.Result{}, nil
	}

	// If we get here, the session is in an unknown state
	log.Info("ChatSession in unknown processing state", "state", chatSession.Status.State)
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// createSmoothAction creates a SmoothAction CRD from LLM response and validated plan
func (r *ChatSessionReconciler) createSmoothAction(
	ctx context.Context,
	chatSession *smoothv1.ChatSession,
	llmResp *llm.LLMResponse,
	executionPlan *planner.Plan,
) error {
	log := logf.FromContext(ctx)

	smoothAction := &smoothv1.SmoothAction{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("action-%s", chatSession.Name),
			Namespace: chatSession.Namespace,
			Annotations: map[string]string{
				"smooth.k8s.io/risk":          executionPlan.RiskAssessment.OverallRisk,
				"smooth.k8s.io/confidence":    fmt.Sprintf("%.0f", executionPlan.RiskAssessment.LLMConfidence*100),
				"smooth.k8s.io/recommendation": executionPlan.RiskAssessment.Recommendation,
			},
		},
		Spec: smoothv1.SmoothActionSpec{
			ChatRef: chatSession.Name,
			Mode:    "suggest", // Default to suggest mode
		},
	}

	// Determine mode based on risk assessment
	canAutoApply := r.Planner.ShouldAutoApply(executionPlan, chatSession.Spec.PreferAuto)
	if canAutoApply {
		smoothAction.Spec.Mode = "auto"
	} else if chatSession.Spec.PreferAuto {
		// User wanted auto but risk is too high
		log.Info("Auto mode requested but blocked by risk assessment",
			"risk", executionPlan.RiskAssessment.OverallRisk,
			"confidence", executionPlan.RiskAssessment.LLMConfidence,
		)
		smoothAction.Spec.Mode = "suggest"
	}

	// Convert LLM response to SmoothAction format
	for _, need := range llmResp.InferredNeeds {
		smoothAction.Spec.InferredNeeds = append(smoothAction.Spec.InferredNeeds,
			smoothv1.InferredNeed{
				Type:     need.Type,
				Reason:   need.Reason,
				Priority: need.Priority,
			},
		)
	}

	for _, patch := range llmResp.Patches {
		smoothAction.Spec.Patches = append(smoothAction.Spec.Patches,
			smoothv1.Patch{
				Kind: patch.Kind,
				YAML: patch.YAML,
			},
		)
	}

	// Set approval requirement for suggest mode
	if smoothAction.Spec.Mode == "suggest" {
		smoothAction.Spec.Approval = smoothv1.ApprovalInfo{
			Required:   true,
			ApprovedBy: "",
		}
	}

	// Create the SmoothAction
	if err := r.Create(ctx, smoothAction); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create SmoothAction: %w", err)
		}
		log.Info("SmoothAction already exists", "name", smoothAction.Name)
		return nil
	}

	log.Info("SmoothAction created successfully",
		"name", smoothAction.Name,
		"mode", smoothAction.Spec.Mode,
		"needs", len(smoothAction.Spec.InferredNeeds),
	)

	return nil
}

// SetupWithManager sets up the controller with the Manager.
// It watches ChatSession CRDs and also watches Deployments, Services, and Ingress
// resources to trigger reconciliation when cluster state changes.
func (r *ChatSessionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&smoothv1.ChatSession{}).
		// Watch Deployments - trigger reconciliation when deployments change
		Watches(
			&appsv1.Deployment{},
			&handler.EnqueueRequestForObject{},
		).
		// Watch Services - trigger reconciliation when services change
		Watches(
			&corev1.Service{},
			&handler.EnqueueRequestForObject{},
		).
		// Watch Ingress - trigger reconciliation when ingress resources change
		Watches(
			&networkingv1.Ingress{},
			&handler.EnqueueRequestForObject{},
		).
		Named("chatsession").
		Complete(r)
}
