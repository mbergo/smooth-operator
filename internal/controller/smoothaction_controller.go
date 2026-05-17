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
	"strings"
	"time"
	"unicode"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	smoothv1 "github.com/mbergo/smooth-operator/api/v1"
	"github.com/mbergo/smooth-operator/internal/executor"
	"github.com/mbergo/smooth-operator/internal/gitops"
)

// SanitizePrompt replaces newlines and control characters with spaces and
// truncates the result to 200 runes to prevent log injection and oversized
// prompts from user-supplied ChatRef values. Truncation is rune-aware so that
// multi-byte UTF-8 characters are never split mid-codepoint.
func SanitizePrompt(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	const maxRunes = 200
	if runes := []rune(s); len(runes) > maxRunes {
		s = string(runes[:maxRunes])
	}
	return s
}

// GitopsAgent is the interface the reconciler uses to persist applied changes to Git.
// Implementations wrap the concrete gitops.Agent and adapt its method signatures to
// what the reconciler has available (the SmoothAction resource).
type GitopsAgent interface {
	GenerateAndCommit(ctx context.Context, action *smoothv1.SmoothAction) (*gitops.GitCommitResult, error)
}

// SmoothActionReconciler reconciles a SmoothAction object
type SmoothActionReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Executor *executor.Executor
	Recorder record.EventRecorder
	GitAgent GitopsAgent
}

// +kubebuilder:rbac:groups=smooth.smooth.k8s.io,resources=smoothactions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=smooth.smooth.k8s.io,resources=smoothactions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=smooth.smooth.k8s.io,resources=smoothactions/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=autoscaling,resources=horizontalpodautoscalers,verbs=get;list;watch;create;update;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// The SmoothAction controller handles execution of validated plans:
// - Suggest mode: Wait for approval, then execute
// - Auto mode: Execute immediately (if risk allows)
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.22.1/pkg/reconcile
func (r *SmoothActionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the SmoothAction instance
	smoothAction := &smoothv1.SmoothAction{}
	err := r.Get(ctx, req.NamespacedName, smoothAction)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("SmoothAction not found, ignoring")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get SmoothAction")
		return ctrl.Result{}, err
	}

	log.Info("Reconciling SmoothAction",
		"name", smoothAction.Name,
		"mode", smoothAction.Spec.Mode,
		"status", smoothAction.Status.State,
	)

	// Initialize status if not set
	if smoothAction.Status.State == "" {
		if smoothAction.Spec.Mode == "auto" {
			smoothAction.Status.State = stateProposed
			r.Recorder.Event(smoothAction, "Normal", "Proposed", "SmoothAction proposed for auto-execution")
		} else {
			smoothAction.Status.State = stateProposed
			r.Recorder.Event(smoothAction, "Normal", "Proposed", "SmoothAction proposed, awaiting approval")
		}

		if err := r.Status().Update(ctx, smoothAction); err != nil {
			log.Error(err, "Failed to update initial status")
			return ctrl.Result{}, err
		}

		return ctrl.Result{Requeue: true}, nil
	}

	// Check if already in terminal state.
	// "GitError" is intentionally excluded so that transient Git failures can be
	// retried on the next requeue; it is a recoverable, non-terminal condition.
	if smoothAction.Status.State == "Applied" ||
		smoothAction.Status.State == "RolledBack" ||
		smoothAction.Status.State == "Declined" ||
		smoothAction.Status.State == "Error" {
		log.Info("SmoothAction in terminal state", "state", smoothAction.Status.State)
		return ctrl.Result{}, nil
	}

	// Reset a previous transient Git failure back to "Proposed" so the
	// execution block below can re-attempt the operation on this requeue.
	if smoothAction.Status.State == "GitError" {
		log.Info("Retrying after GitError; resetting state to Proposed")
		smoothAction.Status.State = stateProposed
		if statusErr := r.Status().Update(ctx, smoothAction); statusErr != nil {
			log.Error(statusErr, "Failed to reset GitError state")
			return ctrl.Result{}, statusErr
		}
		// Fall through to execute immediately on this same reconcile pass.
	}

	// SUGGEST MODE: Wait for approval
	if smoothAction.Spec.Mode == "suggest" && smoothAction.Status.State == stateProposed {
		if smoothAction.Spec.Approval.Required && smoothAction.Spec.Approval.ApprovedBy == "" {
			log.Info("Waiting for approval (suggest mode)")
			// Requeue to check for approval
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}

		if smoothAction.Spec.Approval.ApprovedBy != "" {
			log.Info("Approval received, proceeding with execution",
				"approvedBy", smoothAction.Spec.Approval.ApprovedBy)
			r.Recorder.Event(smoothAction, "Normal", "Approved",
				fmt.Sprintf("Approved by %s", smoothAction.Spec.Approval.ApprovedBy))
		}
	}

	// AUTO MODE or APPROVED SUGGEST MODE: Execute
	if (smoothAction.Spec.Mode == "auto" || smoothAction.Spec.Approval.ApprovedBy != "") &&
		smoothAction.Status.State == stateProposed {

		log.Info("Executing SmoothAction", "mode", smoothAction.Spec.Mode)

		// Update status: mark as Applied and record the timestamp.
		smoothAction.Status.State = "Applied"
		smoothAction.Status.AppliedAt = metav1.Now().Format(time.RFC3339)
		smoothAction.Status.Errors = []string{}

		// Persist applied changes to Git via the GitOps agent when one is configured.
		if r.GitAgent != nil {
			commitResult, gitErr := r.GitAgent.GenerateAndCommit(ctx, smoothAction)
			if gitErr != nil {
				log.Error(gitErr, "GitOps agent failed; recording error and requeueing")
				// Use "GitError" (not "Error") so the terminal-state guard does not
				// short-circuit retries on the next requeue.
				smoothAction.Status.State = "GitError"
				smoothAction.Status.Errors = append(smoothAction.Status.Errors, fmt.Sprintf("git: %v", gitErr))
				r.Recorder.Event(smoothAction, "Warning", "GitOpsFailed", gitErr.Error())

				if statusErr := r.Status().Update(ctx, smoothAction); statusErr != nil {
					log.Error(statusErr, "Failed to persist Error status")
					return ctrl.Result{}, statusErr
				}
				// Requeue with backoff so a transient Git failure can be retried.
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}

			smoothAction.Status.Git = smoothv1.GitInfo{
				Commit: commitResult.CommitSHA,
				Branch: commitResult.Branch,
				PRURL:  commitResult.PRURL,
			}
			if len(commitResult.Errors) > 0 {
				smoothAction.Status.Errors = append(smoothAction.Status.Errors, commitResult.Errors...)
			}
		}

		r.Recorder.Event(smoothAction, "Normal", "Applied",
			fmt.Sprintf("Successfully applied %d manifests", len(smoothAction.Spec.Patches)))

		log.Info("SmoothAction executed successfully",
			"commit", smoothAction.Status.Git.Commit,
			"branch", smoothAction.Status.Git.Branch,
			"prURL", smoothAction.Status.Git.PRURL,
		)

		if err := r.Status().Update(ctx, smoothAction); err != nil {
			log.Error(err, "Failed to update status to Applied")
			return ctrl.Result{}, err
		}

		return ctrl.Result{}, nil
	}

	// If we get here, unknown state
	log.Info("SmoothAction in unknown state", "state", smoothAction.Status.State)
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *SmoothActionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&smoothv1.SmoothAction{}).
		Named("smoothaction").
		Complete(r)
}
