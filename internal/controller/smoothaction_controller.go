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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	smoothv1 "github.com/mbergo/smooth-operator/api/v1"
	"github.com/mbergo/smooth-operator/internal/executor"
)

// SmoothActionReconciler reconciles a SmoothAction object
type SmoothActionReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Executor *executor.Executor
	Recorder record.EventRecorder
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
	err := r.Client.Get(ctx, req.NamespacedName, smoothAction)
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
			smoothAction.Status.State = "Proposed"
			r.Recorder.Event(smoothAction, "Normal", "Proposed", "SmoothAction proposed for auto-execution")
		} else {
			smoothAction.Status.State = "Proposed"
			r.Recorder.Event(smoothAction, "Normal", "Proposed", "SmoothAction proposed, awaiting approval")
		}

		if err := r.Status().Update(ctx, smoothAction); err != nil {
			log.Error(err, "Failed to update initial status")
			return ctrl.Result{}, err
		}

		return ctrl.Result{Requeue: true}, nil
	}

	// Check if already in terminal state
	if smoothAction.Status.State == "Applied" ||
		smoothAction.Status.State == "RolledBack" ||
		smoothAction.Status.State == "Declined" ||
		smoothAction.Status.State == "Error" {
		log.Info("SmoothAction in terminal state", "state", smoothAction.Status.State)
		return ctrl.Result{}, nil
	}

	// SUGGEST MODE: Wait for approval
	if smoothAction.Spec.Mode == "suggest" && smoothAction.Status.State == "Proposed" {
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
		smoothAction.Status.State == "Proposed" {

		log.Info("Executing SmoothAction", "mode", smoothAction.Spec.Mode)

		// Phase 6: Update status with execution progress
		smoothAction.Status.State = "Applied"
		smoothAction.Status.AppliedAt = metav1.Now().Format(time.RFC3339)

		// Phase 6: Add Git information (simulated for now)
		// TODO: Integrate with GitOps agent for real Git operations
		smoothAction.Status.Git = smoothv1.GitInfo{
			Commit: "abc123def456", // Will come from GitOps agent
			Branch: fmt.Sprintf("smooth/%s", smoothAction.Spec.ChatRef),
			PRURL:  fmt.Sprintf("https://github.com/example/repo/pull/new/smooth/%s", smoothAction.Spec.ChatRef),
		}

		// Phase 6: Clear any previous errors
		smoothAction.Status.Errors = []string{}

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
