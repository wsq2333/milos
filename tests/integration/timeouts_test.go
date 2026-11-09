//go:build integration

package integration

import (
	"context"
	milosv1 "github.com/wsq2333/milos/api/v1alpha1"
	"github.com/wsq2333/milos/internal/backends/nativepod"
	"github.com/wsq2333/milos/internal/controller"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"testing"
	"time"
)

func testNodeTimeoutAPI(t *testing.T, c client.Client, namespace string) {
	ctx := context.Background()
	total, runtime, zero := int64(60), int64(10), int64(0)
	run := &milosv1.WorkflowRun{ObjectMeta: metav1.ObjectMeta{Name: "node-timeout", Namespace: namespace}, Spec: milosv1.WorkflowRunSpec{
		ExecutionPolicy: milosv1.ExecutionPolicy{Parallelism: 1, NodeTimeouts: &milosv1.NodeTimeouts{TotalSeconds: &total, RunSeconds: &runtime}},
		Definition:      milosv1.WorkflowDefinition{Nodes: []milosv1.NodeSpec{{Name: "one", Timeouts: &milosv1.NodeTimeouts{TotalSeconds: &zero}, Container: milosv1.ContainerSpec{Image: "test"}}, {Name: "two", Dependencies: []string{"one"}, Container: milosv1.ContainerSpec{Image: "test"}}}},
	}}
	// Typed zero-value parents are supplied explicitly; use the same immutable execution defaults.
	run.Spec.DesiredState = "Running"
	run.Spec.Execution = milosv1.ExecutionConfig{Backend: "native-pod", ContractVersion: "v1", Admission: milosv1.AdmissionConfig{Provider: "None"}, Placement: milosv1.PlacementConfig{Provider: "Local"}, Scheduler: milosv1.SchedulerConfig{Name: "default-scheduler"}}
	for i, value := range []int64{-1, 604801} {
		bad := run.DeepCopy()
		bad.Name = []string{"negative-timeout", "excess-timeout"}[i]
		bad.Spec.Definition.Nodes[0].Timeouts.RunSeconds = &value
		if err := c.Create(ctx, bad); !apierrors.IsInvalid(err) {
			t.Fatalf("invalid node timeout accepted: %v", err)
		}
		bad.Spec.Definition.Nodes[0].Timeouts = nil
		bad.Spec.ExecutionPolicy.NodeTimeouts.RunSeconds = &value
		if err := c.Create(ctx, bad); !apierrors.IsInvalid(err) {
			t.Fatalf("invalid default timeout accepted: %v", err)
		}
	}
	if err := c.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	key := client.ObjectKeyFromObject(run)
	if err := c.Get(ctx, key, run); err != nil {
		t.Fatal(err)
	}
	if run.Spec.Definition.Nodes[0].Timeouts.TotalSeconds == nil || *run.Spec.Definition.Nodes[0].Timeouts.TotalSeconds != 0 || run.Spec.Definition.Nodes[0].Timeouts.RunSeconds != nil {
		t.Fatal("API lost zero versus absent semantics")
	}
	for _, nodeOverride := range []bool{false, true} {
		changed := run.DeepCopy()
		v := int64(20)
		if nodeOverride {
			changed.Spec.Definition.Nodes[0].Timeouts.RunSeconds = &v
		} else {
			changed.Spec.ExecutionPolicy.NodeTimeouts.RunSeconds = &v
		}
		if err := c.Update(ctx, changed); !apierrors.IsInvalid(err) {
			t.Fatalf("timeout policy mutated: %v", err)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	step := func() {
		t.Helper()
		// Reconstruct the controller every time, against a real status subresource.
		r := &controller.WorkflowRunReconciler{Client: c, Reader: c, Backend: &nativepod.Backend{Client: c, Reader: c}, Now: func() time.Time { return now }}
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatal(err)
		}
		if err := c.Get(ctx, key, run); err != nil {
			t.Fatal(err)
		}
	}
	for range 30 {
		step()
		if run.Status.Nodes["one"].PodUID != "" {
			break
		}
	}
	status := run.Status.Nodes["one"]
	if status.PodUID == "" || status.IntentRecordedAt == nil {
		t.Fatal("missing persisted execution intent")
	}
	p := &corev1.Pod{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: status.PodName}, p); err != nil {
		t.Fatal(err)
	}
	p.Status.Phase = corev1.PodPending
	p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable", Message: "Insufficient cpu", LastTransitionTime: metav1.NewTime(now)}}
	if err := c.Status().Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	step()
	if s := run.Status.Nodes["one"]; s.Stage != "WaitingForScheduling" || s.Reason != "Unschedulable" || s.Message != "Insufficient cpu" || s.StageSince == nil {
		t.Fatalf("waiting diagnostics not persisted: %+v", s)
	}
	p.Status.Phase = corev1.PodRunning
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", Image: "test", ImageID: "test", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now)}}}}
	if err := c.Status().Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	step()
	if run.Status.Nodes["one"].StartedAt == nil {
		t.Fatal("runtime timestamp pruned")
	}
	now = now.Add(10 * time.Second)
	step()
	if s := run.Status.Nodes["one"]; s.StopReason != "RunDeadlineExceeded" || s.StopRequestedAt == nil || s.Phase != "Cancelling" {
		t.Fatalf("timeout intent not persisted: %+v", s)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(p), &corev1.Pod{}); err != nil {
		t.Fatal("deleted before persisting intent", err)
	}
	for range 30 {
		step()
		if run.Status.Phase == "Failed" {
			break
		}
	}
	if run.Status.Phase != "Failed" || !run.Status.Nodes["one"].CleanupComplete || run.Status.Nodes["two"].Phase != "Skipped" {
		t.Fatalf("timeout did not converge: %+v", run.Status)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(p), &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("Pod remains: %v", err)
	}
}
