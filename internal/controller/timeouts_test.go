package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	milosv1 "github.com/wsq2333/milos/api/v1alpha1"
	"github.com/wsq2333/milos/internal/backends/nativepod"
	"github.com/wsq2333/milos/internal/execution"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func seconds(v int64) *int64 { return &v }
func (h *harness) spec(f func(*milosv1.WorkflowRunSpec)) {
	h.t.Helper()
	r := h.run()
	f(&r.Spec)
	if err := h.c.Update(h.ctx, r); err != nil {
		h.t.Fatal(err)
	}
}
func (h *harness) podStatus(node string, f func(*corev1.Pod)) {
	h.t.Helper()
	s := h.run().Status.Nodes[node]
	p := &corev1.Pod{}
	if err := h.c.Get(h.ctx, client.ObjectKey{Namespace: h.key.Namespace, Name: s.PodName}, p); err != nil {
		h.t.Fatal(err)
	}
	f(p)
	if err := h.c.Status().Update(h.ctx, p); err != nil {
		h.t.Fatal(err)
	}
}

func TestNodeTimeoutBeforeCreate(t *testing.T) {
	h := newHarness(t)
	h.spec(func(s *milosv1.WorkflowRunSpec) {
		s.ExecutionPolicy.NodeTimeouts = &milosv1.NodeTimeouts{TotalSeconds: seconds(10)}
	})
	h.until(func(r *milosv1.WorkflowRun) bool { return r.Status.Nodes["prepare"].ExecutionIntentRecorded })
	started := h.run().Status.Nodes["prepare"].IntentRecordedAt.DeepCopy()
	h.now = h.now.Add(10 * time.Second)
	h.step()
	s := h.run().Status.Nodes["prepare"]
	if s.StopReason != "NodeDeadlineExceeded" || s.Phase != "Cancelling" || len(h.pods()) != 0 {
		t.Fatalf("missing persisted stop intent: %+v", s)
	}
	h.until(func(r *milosv1.WorkflowRun) bool { return r.Status.Phase == "Failed" })
	s = h.run().Status.Nodes["prepare"]
	if !s.IntentRecordedAt.Equal(started) || !s.CleanupComplete || len(h.pods()) != 0 {
		t.Fatal("timeout reset or created execution")
	}
	if h.run().Status.Nodes["left"].Phase != "Skipped" {
		t.Fatal("successor was not skipped")
	}
}

func TestRunTimeoutExcludesQueueWaitAndSurvivesRestart(t *testing.T) {
	h := newHarness(t)
	h.spec(func(s *milosv1.WorkflowRunSpec) {
		s.ExecutionPolicy.NodeTimeouts = &milosv1.NodeTimeouts{RunSeconds: seconds(10)}
	})
	h.waitPod("prepare")
	h.now = h.now.Add(time.Hour)
	h.step()
	if s := h.run().Status.Nodes["prepare"]; s.StopReason != "" || s.StartedAt != nil || s.Stage != "WaitingForScheduling" {
		t.Fatalf("queue consumed runtime: %+v", s)
	}
	started := h.now
	h.podStatus("prepare", func(p *corev1.Pod) {
		p.Status.Phase = corev1.PodRunning
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(started)}}}}
	})
	h.step()
	h.now = h.now.Add(9 * time.Second)
	h.step()
	if h.run().Status.Nodes["prepare"].StopReason != "" {
		t.Fatal("expired early")
	}
	h.now = h.now.Add(time.Second)
	h.step()
	if s := h.run().Status.Nodes["prepare"]; s.StopReason != "RunDeadlineExceeded" || !s.StartedAt.Time.Equal(started) {
		t.Fatalf("lost runtime clock: %+v", s)
	}
	h.until(func(r *milosv1.WorkflowRun) bool { return r.Status.Phase == "Failed" })
	if len(h.pods()) != 0 {
		t.Fatal("timed out Pod retained")
	}
}

func TestNodeTimeoutExcludesDependenciesAndContinuesIndependentBranch(t *testing.T) {
	h := newHarness(t)
	h.spec(func(s *milosv1.WorkflowRunSpec) {
		s.Definition.Nodes[1].Timeouts = &milosv1.NodeTimeouts{TotalSeconds: seconds(10)}
	})
	h.waitPod("prepare")
	h.now = h.now.Add(time.Hour)
	h.step()
	if h.run().Status.Nodes["left"].IntentRecordedAt != nil {
		t.Fatal("dependency wait started node clock")
	}
	h.finish("prepare", corev1.PodSucceeded)
	h.waitPod("left")
	h.waitPod("right")
	h.now = h.now.Add(10 * time.Second)
	h.until(func(r *milosv1.WorkflowRun) bool { return r.Status.Nodes["left"].Phase == "Failed" })
	h.until(func(r *milosv1.WorkflowRun) bool { return r.Status.Nodes["finish"].Phase == "Skipped" })
	if h.run().Status.Phase != "Running" || h.run().Status.Nodes["right"].StopReason != "" {
		t.Fatal("independent branch stopped")
	}
	h.finish("right", corev1.PodSucceeded)
	h.until(func(r *milosv1.WorkflowRun) bool { return r.Status.Phase == "Failed" })
}

func TestTimeoutWaitsForCleanupAndHoldsSlot(t *testing.T) {
	h := newHarness(t)
	h.spec(func(s *milosv1.WorkflowRunSpec) {
		s.Definition.Nodes = []milosv1.NodeSpec{{Name: "a", Timeouts: &milosv1.NodeTimeouts{TotalSeconds: seconds(10)}, Container: milosv1.ContainerSpec{Image: "test"}}, {Name: "b", Container: milosv1.ContainerSpec{Image: "test"}}, {Name: "c", Container: milosv1.ContainerSpec{Image: "test"}}}
	})
	h.waitPod("a")
	h.waitPod("b")
	s := h.run().Status.Nodes["a"]
	p := &corev1.Pod{}
	if err := h.c.Get(h.ctx, client.ObjectKey{Namespace: h.key.Namespace, Name: s.PodName}, p); err != nil {
		t.Fatal(err)
	}
	p.Finalizers = []string{"test.example.io/hold"}
	if err := h.c.Update(h.ctx, p); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(10 * time.Second)
	for range 6 {
		h.step()
	}
	r := h.run()
	if r.Status.Nodes["a"].Phase != "Cancelling" || r.Status.Nodes["a"].CleanupComplete || r.Status.Nodes["c"].ExecutionIntentRecorded {
		t.Fatalf("slot released before cleanup: %+v", r.Status)
	}
	if r.Status.Nodes["c"].Stage != "WaitingForParallelism" {
		t.Fatal("parallelism wait not visible")
	}
	// Finishing another node releases its slot even while timeout cleanup is blocked.
	h.finish("b", corev1.PodSucceeded)
	h.waitPod("c")
	if err := h.c.Get(h.ctx, client.ObjectKeyFromObject(p), p); err != nil {
		t.Fatal(err)
	}
	p.Finalizers = nil
	if err := h.c.Update(h.ctx, p); err != nil {
		t.Fatal(err)
	}
	h.until(func(r *milosv1.WorkflowRun) bool { return r.Status.Nodes["a"].Phase == "Failed" })
}

func TestCompletionDeadlineRace(t *testing.T) {
	for _, test := range []struct {
		name    string
		finish  int
		timeout bool
	}{{"before", 9, false}, {"at", 10, false}, {"after", 11, true}} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			h.spec(func(s *milosv1.WorkflowRunSpec) {
				s.ExecutionPolicy.NodeTimeouts = &milosv1.NodeTimeouts{RunSeconds: seconds(10)}
			})
			h.waitPod("prepare")
			started := h.now
			h.now = h.now.Add(20 * time.Second)
			h.podStatus("prepare", func(p *corev1.Pod) {
				p.Status.Phase = corev1.PodSucceeded
				p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{StartedAt: metav1.NewTime(started), FinishedAt: metav1.NewTime(started.Add(time.Duration(test.finish) * time.Second))}}}}
			})
			h.step()
			s := h.run().Status.Nodes["prepare"]
			if (s.StopReason != "") != test.timeout {
				t.Fatalf("wrong deadline outcome: %+v", s)
			}
			if !test.timeout && s.Phase != "Succeeded" {
				t.Fatalf("lost timely completion: %+v", s)
			}
		})
	}
}

func TestTimeoutIntentConflictDoesNotDeletePod(t *testing.T) {
	h := newHarness(t)
	h.spec(func(s *milosv1.WorkflowRunSpec) {
		s.ExecutionPolicy.NodeTimeouts = &milosv1.NodeTimeouts{TotalSeconds: seconds(10)}
	})
	h.waitPod("prepare")
	h.now = h.now.Add(10 * time.Second)
	r := h.reconciler()
	r.Client = interceptor.NewClient(h.c.(client.WithWatch), interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
		return apierrors.NewConflict(schema.GroupResource{Group: "milos.example.io", Resource: "workflowruns"}, obj.GetName(), errors.New("conflict"))
	}})
	if _, err := r.Reconcile(h.ctx, ctrl.Request{NamespacedName: h.key}); !apierrors.IsConflict(err) {
		t.Fatalf("expected conflict: %v", err)
	}
	if len(h.pods()) != 1 || h.run().Status.Nodes["prepare"].StopReason != "" {
		t.Fatal("side effect before stop intent commit")
	}
	h.step()
	// Even if a completion arrives later, the committed stop decision is irreversible.
	h.finish("prepare", corev1.PodSucceeded)
	h.until(func(r *milosv1.WorkflowRun) bool { return r.Status.Phase == "Failed" })
	if h.run().Status.Nodes["prepare"].Reason != "NodeDeadlineExceeded" {
		t.Fatal("late result replaced stop reason")
	}
}

type unavailableBackend struct {
	execution.Backend
	blockCleanup bool
}

func (b *unavailableBackend) Observe(context.Context, execution.Request, execution.Handle) (execution.Observation, error) {
	return execution.Observation{}, errors.New("worker unavailable")
}
func (b *unavailableBackend) Stop(ctx context.Context, r execution.Request, h execution.Handle) (execution.Update, error) {
	if b.blockCleanup {
		return execution.Update{}, errors.New("cannot confirm worker cleanup")
	}
	return b.Backend.Stop(ctx, r, h)
}
func TestObservationOutageDoesNotDisableTimeout(t *testing.T) {
	h := newHarness(t)
	h.spec(func(s *milosv1.WorkflowRunSpec) {
		s.ExecutionPolicy.NodeTimeouts = &milosv1.NodeTimeouts{TotalSeconds: seconds(10)}
	})
	h.waitPod("prepare")
	b := &unavailableBackend{Backend: &nativepod.Backend{Client: h.c, Reader: h.c}, blockCleanup: true}
	h.backend = b
	h.step()
	if s := h.run().Status.Nodes["prepare"]; s.Stage != "ObservationUnavailable" || s.Message != "worker unavailable" {
		t.Fatalf("missing diagnostics: %+v", s)
	}
	h.now = h.now.Add(10 * time.Second)
	h.step()
	h.step()
	if s := h.run().Status.Nodes["prepare"]; s.Phase != "Cancelling" || s.CleanupComplete || s.Message != "cannot confirm worker cleanup" {
		t.Fatalf("outage falsely completed cleanup: %+v", s)
	}
	b.blockCleanup = false
	h.until(func(r *milosv1.WorkflowRun) bool { return r.Status.Phase == "Failed" })
}

func TestTimeoutDefaultsOverridesAndValidation(t *testing.T) {
	defaults := &milosv1.NodeTimeouts{TotalSeconds: seconds(100), RunSeconds: seconds(10)}
	total, run := nodeTimeouts(defaults, &milosv1.NodeTimeouts{TotalSeconds: seconds(0)})
	if total != 0 || run != 10 {
		t.Fatal("zero override must disable only its own limit")
	}
	for _, value := range []int64{-1, 604801} {
		h := newHarness(t)
		r := h.run()
		r.Spec.ExecutionPolicy.NodeTimeouts = &milosv1.NodeTimeouts{RunSeconds: seconds(value)}
		if _, _, err := prepare(r); err == nil {
			t.Fatal("invalid default accepted")
		}
		r.Spec.ExecutionPolicy.NodeTimeouts = nil
		r.Spec.Definition.Nodes[0].Timeouts = &milosv1.NodeTimeouts{TotalSeconds: seconds(value)}
		if _, _, err := prepare(r); err == nil {
			t.Fatal("invalid override accepted")
		}
	}
	h := newHarness(t)
	h.spec(func(s *milosv1.WorkflowRunSpec) {
		s.ExecutionPolicy.NodeTimeouts = defaults
		s.Definition.Nodes[0].Timeouts = &milosv1.NodeTimeouts{TotalSeconds: seconds(0), RunSeconds: seconds(0)}
	})
	h.waitPod("prepare")
	h.now = h.now.Add(time.Hour)
	h.step()
	if h.run().Status.Nodes["prepare"].StopReason != "" {
		t.Fatal("disabled override expired")
	}
}

func TestEarliestNodeBudgetWins(t *testing.T) {
	for _, test := range []struct {
		total, run int64
		reason     string
	}{{5, 10, "NodeDeadlineExceeded"}, {30, 10, "RunDeadlineExceeded"}, {10, 10, "NodeDeadlineExceeded"}} {
		t.Run(test.reason+(time.Duration(test.total)*time.Second).String(), func(t *testing.T) {
			h := newHarness(t)
			h.spec(func(s *milosv1.WorkflowRunSpec) {
				s.ExecutionPolicy.NodeTimeouts = &milosv1.NodeTimeouts{TotalSeconds: seconds(test.total), RunSeconds: seconds(test.run)}
			})
			h.waitPod("prepare")
			h.podStatus("prepare", func(p *corev1.Pod) {
				p.Status.Phase = corev1.PodRunning
				p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(h.now)}}}}
			})
			h.now = h.now.Add(10 * time.Second)
			h.step()
			if s := h.run().Status.Nodes["prepare"]; s.StopReason != test.reason {
				t.Fatalf("wrong winning budget: %+v", s)
			}
		})
	}
}

func TestWaitingStageClockDoesNotReset(t *testing.T) {
	h := newHarness(t)
	h.waitPod("prepare")
	h.step()
	initial := h.run().Status.Nodes["prepare"].StageSince.DeepCopy()
	h.now = h.now.Add(time.Minute)
	h.podStatus("prepare", func(p *corev1.Pod) {
		p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable", Message: "Insufficient cpu"}}
	})
	h.step()
	s := h.run().Status.Nodes["prepare"]
	if !s.StageSince.Equal(initial) || s.Reason != "Unschedulable" || s.Message != "Insufficient cpu" {
		t.Fatalf("waiting clock reset or diagnostic missing: %+v", s)
	}
}

type createErrorBackend struct{ execution.Backend }

func (b *createErrorBackend) Ensure(context.Context, execution.Request, execution.Handle) (execution.Update, error) {
	return execution.Update{}, errors.New("quota rejected resource creation")
}
func TestCreateErrorVisibleUntilTimeout(t *testing.T) {
	h := newHarness(t)
	h.spec(func(s *milosv1.WorkflowRunSpec) {
		s.ExecutionPolicy.NodeTimeouts = &milosv1.NodeTimeouts{TotalSeconds: seconds(10)}
	})
	h.backend = &createErrorBackend{Backend: &nativepod.Backend{Client: h.c, Reader: h.c}}
	for range 10 {
		r := h.reconciler()
		_, err := r.Reconcile(h.ctx, ctrl.Request{NamespacedName: h.key})
		if err != nil {
			break
		}
	}
	s := h.run().Status.Nodes["prepare"]
	if s.Reason != "CreationError" || s.Message != "quota rejected resource creation" {
		t.Fatalf("missing creation error: %+v", s)
	}
	h.now = h.now.Add(10 * time.Second)
	h.until(func(r *milosv1.WorkflowRun) bool { return r.Status.Phase == "Failed" })
	if h.run().Status.Nodes["prepare"].StopReason != "NodeDeadlineExceeded" || len(h.pods()) != 0 {
		t.Fatal("creation failure bypassed total deadline")
	}
}

func TestWorkflowDeadlineOverridesNodeBudgets(t *testing.T) {
	h := newHarness(t)
	h.spec(func(s *milosv1.WorkflowRunSpec) {
		s.ExecutionPolicy.WorkflowTimeoutSeconds = 10
		s.ExecutionPolicy.NodeTimeouts = &milosv1.NodeTimeouts{TotalSeconds: seconds(100)}
	})
	h.waitPod("prepare")
	h.now = h.run().CreationTimestamp.Add(10 * time.Second)
	h.until(func(r *milosv1.WorkflowRun) bool { return r.Status.Phase == "Failed" })
	if h.run().Status.StopReason != "DeadlineExceeded" || len(h.pods()) != 0 {
		t.Fatal("node budget extended the workflow deadline")
	}
}
