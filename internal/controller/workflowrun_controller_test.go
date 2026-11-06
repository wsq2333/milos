package controller

import (
	"context"
	"fmt"
	"testing"
	"time"

	milosv1 "github.com/wsq2333/milos/api/v1alpha1"
	"github.com/wsq2333/milos/internal/backends/nativepod"
	"github.com/wsq2333/milos/internal/workflow"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

type harness struct {
	t   *testing.T
	ctx context.Context
	c   client.Client
	key client.ObjectKey
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := milosv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	run := &milosv1.WorkflowRun{ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "workflow-uid", CreationTimestamp: metav1.Now()}, Spec: milosv1.WorkflowRunSpec{
		DesiredState:    "Running",
		Execution:       milosv1.ExecutionConfig{Backend: "native-pod", ContractVersion: "v1", Admission: milosv1.AdmissionConfig{Provider: "None"}, Placement: milosv1.PlacementConfig{Provider: "Local"}, Scheduler: milosv1.SchedulerConfig{Name: "default-scheduler"}},
		ExecutionPolicy: milosv1.ExecutionPolicy{Parallelism: 2},
		Definition: milosv1.WorkflowDefinition{Nodes: []milosv1.NodeSpec{
			{Name: "prepare", Container: milosv1.ContainerSpec{Image: "test"}},
			{Name: "left", Dependencies: []string{"prepare"}, Container: milosv1.ContainerSpec{Image: "test"}},
			{Name: "right", Dependencies: []string{"prepare"}, Container: milosv1.ContainerSpec{Image: "test"}},
			{Name: "finish", Dependencies: []string{"left", "right"}, Container: milosv1.ContainerSpec{Image: "test"}},
		}},
	}}
	nextUID := 0
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&milosv1.WorkflowRun{}, &corev1.Pod{}).WithObjects(run).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			nextUID++
			obj.SetUID(types.UID(fmt.Sprintf("pod-%d", nextUID)))
			return c.Create(ctx, obj, opts...)
		},
	}).Build()
	return &harness{t: t, ctx: context.Background(), c: c, key: client.ObjectKeyFromObject(run)}
}

func (h *harness) step() {
	h.t.Helper()
	// Reconstruct the controller and backend on every iteration to exercise recovery.
	r := &WorkflowRunReconciler{Client: h.c, Reader: h.c, Backend: &nativepod.Backend{Client: h.c, Reader: h.c}}
	if _, err := r.Reconcile(h.ctx, ctrl.Request{NamespacedName: h.key}); err != nil {
		h.t.Fatal(err)
	}
}
func (h *harness) run() *milosv1.WorkflowRun {
	h.t.Helper()
	r := &milosv1.WorkflowRun{}
	if err := h.c.Get(h.ctx, h.key, r); err != nil {
		h.t.Fatal(err)
	}
	return r
}
func (h *harness) until(pred func(*milosv1.WorkflowRun) bool) {
	h.t.Helper()
	for range 40 {
		if pred(h.run()) {
			return
		}
		h.step()
	}
	h.t.Fatalf("did not converge: %+v", h.run().Status)
}
func (h *harness) waitPod(name string) {
	h.until(func(r *milosv1.WorkflowRun) bool { return r.Status.Nodes[name].PodUID != "" })
}
func (h *harness) finish(name string, phase corev1.PodPhase) {
	h.t.Helper()
	s := h.run().Status.Nodes[name]
	p := &corev1.Pod{}
	if err := h.c.Get(h.ctx, client.ObjectKey{Namespace: h.key.Namespace, Name: s.PodName}, p); err != nil {
		h.t.Fatal(err)
	}
	p.Status.Phase = phase
	if err := h.c.Status().Update(h.ctx, p); err != nil {
		h.t.Fatal(err)
	}
}
func (h *harness) pods() []corev1.Pod {
	h.t.Helper()
	list := &corev1.PodList{}
	if err := h.c.List(h.ctx, list); err != nil {
		h.t.Fatal(err)
	}
	return list.Items
}

func TestDiamondPersistsResultBeforeSuccessors(t *testing.T) {
	h := newHarness(t)
	h.waitPod("prepare")
	if len(h.pods()) != 1 {
		t.Fatal("successor started before its dependency")
	}
	h.finish("prepare", corev1.PodSucceeded)
	h.step()
	r := h.run()
	if r.Status.Nodes["prepare"].Phase != "Succeeded" || r.Status.Nodes["left"].ExecutionIntentRecorded || len(h.pods()) != 1 {
		t.Fatal("result was not persisted separately from successor intent")
	}
	h.waitPod("left")
	h.waitPod("right")
	h.finish("left", corev1.PodSucceeded)
	h.finish("right", corev1.PodSucceeded)
	h.waitPod("finish")
	h.finish("finish", corev1.PodSucceeded)
	h.until(func(r *milosv1.WorkflowRun) bool { return r.Status.Phase == "Succeeded" })
	if len(h.pods()) != 4 {
		t.Fatal("unexpected duplicate or premature collection")
	}
}

func TestFailureContinuesIndependentBranch(t *testing.T) {
	h := newHarness(t)
	h.waitPod("prepare")
	h.finish("prepare", corev1.PodSucceeded)
	h.waitPod("left")
	h.waitPod("right")
	h.finish("left", corev1.PodFailed)
	h.until(func(r *milosv1.WorkflowRun) bool { return r.Status.Nodes["finish"].Phase == "Skipped" })
	if h.run().Status.Phase != "Running" {
		t.Fatal("independent branch stopped")
	}
	h.finish("right", corev1.PodSucceeded)
	h.until(func(r *milosv1.WorkflowRun) bool { return r.Status.Phase == "Failed" })
	if len(h.pods()) != 3 {
		t.Fatal("failed successor ran")
	}
}

func TestCancelAfterCreateBeforeHandleCommit(t *testing.T) {
	h := newHarness(t)
	h.until(func(r *milosv1.WorkflowRun) bool { return r.Status.Nodes["prepare"].ExecutionIntentRecorded })
	run := h.run()
	s := run.Status.Nodes["prepare"]
	b := &nativepod.Backend{Client: h.c, Reader: h.c}
	if _, err := b.Ensure(h.ctx, request(run, run.Spec.Definition.Nodes[0], s), executionHandle(s)); err != nil {
		t.Fatal(err)
	}
	// The Pod exists, but its UID has not yet been committed to WorkflowRun status.
	run.Spec.DesiredState = "Cancelled"
	if err := h.c.Update(h.ctx, run); err != nil {
		t.Fatal(err)
	}
	h.until(func(r *milosv1.WorkflowRun) bool { return r.Status.Phase == "Cancelled" })
	if len(h.pods()) != 0 {
		t.Fatal("cancel leaked a Pod whose handle was not committed")
	}
	for _, s := range h.run().Status.Nodes {
		if !s.CleanupComplete {
			t.Fatal("cleanup was not confirmed")
		}
	}
}

func TestMissingPodDoesNotRetry(t *testing.T) {
	h := newHarness(t)
	h.waitPod("prepare")
	p := h.pods()[0]
	if err := h.c.Delete(h.ctx, &p); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		h.step()
	}
	s := h.run().Status.Nodes["prepare"]
	if len(h.pods()) != 0 || s.Reason != "ResourceMissing" || workflow.Terminal(workflow.Phase(s.Phase)) {
		t.Fatalf("missing Pod was retried or marked terminal: %+v", s)
	}
}

func TestDeletionCleansUpBeforeRemovingFinalizer(t *testing.T) {
	h := newHarness(t)
	h.waitPod("prepare")
	if err := h.c.Delete(h.ctx, h.run()); err != nil {
		t.Fatal(err)
	}
	for range 10 {
		h.step()
		err := h.c.Get(h.ctx, h.key, &milosv1.WorkflowRun{})
		if apierrors.IsNotFound(err) {
			if len(h.pods()) != 0 {
				t.Fatal("removed finalizer before cleanup")
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("deletion did not converge")
}

func TestWorkflowDeadline(t *testing.T) {
	h := newHarness(t)
	run := h.run()
	run.Spec.ExecutionPolicy.WorkflowTimeoutSeconds = 1
	run.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))
	if err := h.c.Update(h.ctx, run); err != nil {
		t.Fatal(err)
	}
	h.until(func(r *milosv1.WorkflowRun) bool { return r.Status.Phase == "Failed" })
	if h.run().Status.StopReason != "DeadlineExceeded" || len(h.pods()) != 0 {
		t.Fatal("expired run started work")
	}
}

func TestInvalidDefinitionDoesNotExecute(t *testing.T) {
	h := newHarness(t)
	run := h.run()
	run.Spec.Definition.Nodes[0].Dependencies = []string{"finish"}
	if err := h.c.Update(h.ctx, run); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		h.step()
	}
	run = h.run()
	if len(h.pods()) != 0 || len(run.Status.Conditions) != 1 || run.Status.Conditions[0].Status != metav1.ConditionFalse {
		t.Fatalf("invalid DAG accepted: %+v", run.Status)
	}
}

func TestDigestIgnoresMapAndSetListOrdering(t *testing.T) {
	h := newHarness(t)
	run := h.run()
	_, original, err := prepare(run)
	if err != nil {
		t.Fatal(err)
	}
	run.Spec.Definition.Nodes[0], run.Spec.Definition.Nodes[1] = run.Spec.Definition.Nodes[1], run.Spec.Definition.Nodes[0]
	run.Spec.Definition.Nodes[3].Dependencies = []string{"right", "left"}
	_, reordered, err := prepare(run)
	if err != nil {
		t.Fatal(err)
	}
	if original != reordered {
		t.Fatal("equivalent map/set list order changed execution digest")
	}
}
