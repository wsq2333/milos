// Package controller coordinates persisted DAG decisions and backend operations.
package controller

import (
	"context"
	"reflect"
	"time"

	milosv1 "github.com/wsq2333/milos/api/v1alpha1"
	"github.com/wsq2333/milos/internal/execution"
	executionpod "github.com/wsq2333/milos/internal/pod"
	"github.com/wsq2333/milos/internal/workflow"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const Finalizer = "milos.example.io/execution-cleanup"
const pollInterval = 5 * time.Second

type WorkflowRunReconciler struct {
	client.Client
	// Reader bypasses the cache for cancellation intent and optimistic status updates.
	Reader  client.Reader
	Backend execution.Backend
}

// +kubebuilder:rbac:groups=milos.example.io,resources=workflowruns,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=milos.example.io,resources=workflowruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=milos.example.io,resources=workflowruns/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *WorkflowRunReconciler) Reconcile(ctx context.Context, key ctrl.Request) (ctrl.Result, error) {
	run := &milosv1.WorkflowRun{}
	if err := r.Reader.Get(ctx, key.NamespacedName, run); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !run.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(run, Finalizer) {
			return ctrl.Result{}, nil
		}
		return r.stop(ctx, run, "Deleted")
	}
	if !controllerutil.ContainsFinalizer(run, Finalizer) {
		before := run.DeepCopy()
		controllerutil.AddFinalizer(run, Finalizer)
		err := r.Patch(ctx, run, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
		return ctrl.Result{RequeueAfter: time.Millisecond}, err
	}
	if workflow.Terminal(workflow.Phase(run.Status.Phase)) {
		return ctrl.Result{}, nil
	}
	if run.Spec.DesiredState == "Cancelled" {
		return r.stop(ctx, run, "UserCancelled")
	}
	timeout := run.Spec.ExecutionPolicy.WorkflowTimeoutSeconds
	if run.Status.StopReason != "" {
		return r.stop(ctx, run, run.Status.StopReason)
	}
	if timeout > 0 && !run.CreationTimestamp.IsZero() && time.Now().After(run.CreationTimestamp.Add(time.Duration(timeout)*time.Second)) {
		return r.stop(ctx, run, "DeadlineExceeded")
	}
	nodes, digest, err := prepare(run)
	if err != nil {
		return r.invalid(ctx, run, err.Error())
	}
	if run.Status.DefinitionDigest != "" && run.Status.DefinitionDigest != digest {
		return r.invalid(ctx, run, "immutable execution inputs changed after initialization")
	}
	before := run.DeepCopy()
	if run.Status.DefinitionDigest == "" {
		run.Status.DefinitionDigest = digest
		run.Status.ObservedGeneration = run.Generation
		run.Status.Phase = string(workflow.Running)
		run.Status.Nodes = make(map[string]milosv1.NodeStatus, len(nodes))
		for _, n := range nodes {
			run.Status.Nodes[n.Name] = milosv1.NodeStatus{Phase: string(workflow.Waiting)}
		}
		meta.SetStatusCondition(&run.Status.Conditions, metav1.Condition{Type: "Accepted", Status: metav1.ConditionTrue, Reason: "ValidDefinition", Message: "The Pod execution contract is valid", ObservedGeneration: run.Generation})
		return r.save(ctx, before, run)
	}
	// Commit observations before planning successors, including recovery after Create.
	for _, node := range run.Spec.Definition.Nodes {
		s := run.Status.Nodes[node.Name]
		if !s.ExecutionIntentRecorded || workflow.Terminal(workflow.Phase(s.Phase)) {
			continue
		}
		observation, err := r.Backend.Observe(ctx, request(run, node, s), executionHandle(s))
		if err != nil {
			return ctrl.Result{}, err
		}
		applyHandle(&s, observation.Handle)
		s.Admission, s.Placement, s.ObservationSource, s.FailureClass = observation.Admission, observation.Placement, observation.Source, observation.FailureClass
		if observation.Available {
			if observation.Phase != "Unknown" {
				s.Phase = observation.Phase
			}
		}
		s.Reason = observation.Reason
		run.Status.Nodes[node.Name] = s
	}
	if !reflect.DeepEqual(before.Status, run.Status) {
		return r.save(ctx, before, run)
	}
	states := make(map[string]workflow.State, len(nodes))
	for name, s := range run.Status.Nodes {
		states[name] = workflow.State{Phase: workflow.Phase(s.Phase), Reserved: s.ExecutionIntentRecorded}
	}
	plan, err := workflow.Compute(nodes, states, int(run.Spec.ExecutionPolicy.Parallelism))
	if err != nil {
		return r.invalid(ctx, run, err.Error())
	}
	for _, name := range plan.Skipped {
		s := run.Status.Nodes[name]
		s.Phase, s.Reason = string(workflow.Skipped), "DependencyFailed"
		run.Status.Nodes[name] = s
	}
	for _, name := range plan.Ready {
		s := run.Status.Nodes[name]
		s.ExecutionIntentRecorded = true
		s.PodName = executionpod.Name(execution.Key{WorkflowUID: run.UID, NodeID: name, Attempt: s.Attempt, DispatchGeneration: s.DispatchGeneration})
		run.Status.Nodes[name] = s
	}
	run.Status.Phase = string(plan.Phase)
	// Persist reservations before Create. A crash replays the same execution identity.
	if !reflect.DeepEqual(before.Status, run.Status) {
		return r.save(ctx, before, run)
	}
	for _, node := range run.Spec.Definition.Nodes {
		s := run.Status.Nodes[node.Name]
		if !s.ExecutionIntentRecorded || s.PodUID != "" || workflow.Terminal(workflow.Phase(s.Phase)) {
			continue
		}
		update, err := r.Backend.Ensure(ctx, request(run, node, s), executionHandle(s))
		if err != nil {
			return ctrl.Result{}, err
		}
		applyHandle(&s, update.Handle)
		if update.Complete {
			s.Phase, s.Reason = string(workflow.Submitted), ""
		}
		run.Status.Nodes[node.Name] = s
		return r.save(ctx, before, run)
	}
	if workflow.Terminal(workflow.Phase(run.Status.Phase)) {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: pollInterval}, nil
}

func (r *WorkflowRunReconciler) save(ctx context.Context, before, run *milosv1.WorkflowRun) (ctrl.Result, error) {
	err := r.Status().Patch(ctx, run, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
	return ctrl.Result{RequeueAfter: time.Millisecond}, err
}

func (r *WorkflowRunReconciler) invalid(ctx context.Context, run *milosv1.WorkflowRun, message string) (ctrl.Result, error) {
	before := run.DeepCopy()
	meta.SetStatusCondition(&run.Status.Conditions, metav1.Condition{Type: "Accepted", Status: metav1.ConditionFalse, Reason: "InvalidDefinition", Message: message, ObservedGeneration: run.Generation})
	if reflect.DeepEqual(before.Status, run.Status) {
		return ctrl.Result{RequeueAfter: pollInterval}, nil
	}
	return r.save(ctx, before, run)
}

func (r *WorkflowRunReconciler) stop(ctx context.Context, run *milosv1.WorkflowRun, reason string) (ctrl.Result, error) {
	before := run.DeepCopy()
	if run.Status.StopReason == "" || run.Status.Phase != string(workflow.Cancelling) {
		run.Status.StopReason, run.Status.Phase = reason, string(workflow.Cancelling)
		return r.save(ctx, before, run)
	}
	if run.Status.Nodes == nil {
		run.Status.Nodes = map[string]milosv1.NodeStatus{}
	}
	complete := true
	for _, node := range run.Spec.Definition.Nodes {
		s := run.Status.Nodes[node.Name]
		if s.CleanupComplete {
			continue
		}
		if s.ExecutionIntentRecorded {
			update, err := r.Backend.Stop(ctx, request(run, node, s), executionHandle(s))
			if err != nil {
				return ctrl.Result{}, err
			}
			applyHandle(&s, update.Handle)
			s.CleanupComplete = update.Complete
		} else {
			s.CleanupComplete = true
		}
		complete = complete && s.CleanupComplete
		if s.CleanupComplete && !workflow.Terminal(workflow.Phase(s.Phase)) {
			s.Phase = string(workflow.Cancelled)
		}
		run.Status.Nodes[node.Name] = s
	}
	if complete {
		if !run.DeletionTimestamp.IsZero() {
			controllerutil.RemoveFinalizer(run, Finalizer)
			err := r.Patch(ctx, run, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
			return ctrl.Result{}, err
		}
		run.Status.Phase = string(workflow.Cancelled)
		if run.Status.StopReason == "DeadlineExceeded" {
			run.Status.Phase = string(workflow.Failed)
		}
	}
	if !reflect.DeepEqual(before.Status, run.Status) {
		return r.save(ctx, before, run)
	}
	return ctrl.Result{RequeueAfter: pollInterval}, nil
}

func (r *WorkflowRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&milosv1.WorkflowRun{}).Owns(&corev1.Pod{}).Complete(r)
}
