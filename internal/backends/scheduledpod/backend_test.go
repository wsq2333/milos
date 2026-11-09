package scheduledpod

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/wsq2333/milos/internal/execution"
	executionpod "github.com/wsq2333/milos/internal/pod"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func testClient(t *testing.T) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	n := 0
	return fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Pod{}).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
			n++
			o.SetUID(types.UID(fmt.Sprintf("uid-%d", n)))
			return c.Create(ctx, o, opts...)
		},
	}).Build()
}
func fixture(t *testing.T) (*Backend, execution.Request) {
	c := testClient(t)
	b := &Backend{Client: c, Reader: c, EnableKueue: true, EnableMultiKueue: true, EnableVolcano: true, Workers: map[string]client.Reader{}, Origin: "multikueue"}
	r := execution.Request{Key: execution.Key{WorkflowUID: "run-uid", NodeID: "train"}, Namespace: "test", WorkflowName: "run", OwnerAPIVersion: "milos.example.io/v1alpha1", Digest: "digest",
		Configuration: execution.Configuration{Backend: execution.KueueBackend, Scheduler: "default-scheduler", QueueName: "local"},
		PodSpec:       corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, SchedulerName: "default-scheduler", Containers: []corev1.Container{{Name: "main", Image: "test"}}},
	}
	createObject(t, c, LocalQueueGVK, "test", "local", map[string]any{"clusterQueue": "cluster"})
	createObject(t, c, ClusterQueueGVK, "", "cluster", map[string]any{})
	return b, r
}
func createObject(t *testing.T, c client.Client, gvk schema.GroupVersionKind, ns, name string, spec map[string]any) *unstructured.Unstructured {
	t.Helper()
	u := object(gvk)
	u.SetName(name)
	u.SetNamespace(ns)
	u.Object["spec"] = spec
	if err := c.Create(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	return u
}
func createManagerPod(t *testing.T, b *Backend, r execution.Request) (*corev1.Pod, execution.Handle) {
	t.Helper()
	u, err := b.Ensure(context.Background(), r, execution.Handle{})
	if err != nil {
		t.Fatal(err)
	}
	p := &corev1.Pod{}
	if err := b.Client.Get(context.Background(), client.ObjectKey{Namespace: r.Namespace, Name: u.Handle.PodName}, p); err != nil {
		t.Fatal(err)
	}
	if len(p.Spec.SchedulingGates) != 1 || p.Spec.SchedulingGates[0].Name != AdmissionGate {
		t.Fatal("missing admission gate")
	}
	p.Labels[ManagedLabel] = "true"
	if err := b.Client.Update(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	return p, u.Handle
}
func createWorkload(t *testing.T, b *Backend, r execution.Request, p *corev1.Pod) *unstructured.Unstructured {
	t.Helper()
	w := object(WorkloadGVK)
	w.SetName("pod-" + p.Name + "-test")
	w.SetNamespace(r.Namespace)
	controller := true
	w.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: p.Name, UID: p.UID, Controller: &controller}})
	w.SetLabels(map[string]string{"kueue.x-k8s.io/job-uid": string(p.UID)})
	w.Object["spec"] = map[string]any{"queueName": r.Configuration.QueueName}
	if err := b.Client.Create(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	return w
}
func setCondition(t *testing.T, c client.Client, w *unstructured.Unstructured, name string) {
	t.Helper()
	w.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": name, "status": "True"}}}
	if err := c.Update(context.Background(), w); err != nil {
		t.Fatal(err)
	}
}
func enableRemote(t *testing.T, b *Backend, r *execution.Request) client.Client {
	t.Helper()
	r.Configuration.Backend = execution.MultiKueueBackend
	r.Configuration.AdmissionCheck = "dispatch"
	r.Configuration.WorkerClusters = []string{"worker"}
	cq, err := get(context.Background(), b.Reader, ClusterQueueGVK, "", "cluster")
	if err != nil {
		t.Fatal(err)
	}
	cq.Object["spec"] = map[string]any{"admissionChecksStrategy": map[string]any{"admissionChecks": []any{map[string]any{"name": "dispatch"}}}}
	if err := b.Client.Update(context.Background(), cq); err != nil {
		t.Fatal(err)
	}
	createObject(t, b.Client, AdmissionCheckGVK, "", "dispatch", map[string]any{"controllerName": MultiController, "parameters": map[string]any{"apiGroup": WorkloadGVK.Group, "kind": "MultiKueueConfig", "name": "workers"}})
	createObject(t, b.Client, MultiConfigGVK, "", "workers", map[string]any{"clusters": []any{"worker"}})
	c := testClient(t)
	b.Workers["worker"] = c
	return c
}
func createRemote(t *testing.T, c client.Client, r execution.Request, p *corev1.Pod, wl *unstructured.Unstructured) *corev1.Pod {
	t.Helper()
	w := object(WorkloadGVK)
	w.SetName(wl.GetName())
	w.SetNamespace(r.Namespace)
	w.SetLabels(map[string]string{OriginLabel: "multikueue"})
	w.Object["spec"] = map[string]any{"queueName": r.Configuration.QueueName}
	if err := c.Create(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	setCondition(t, c, w, "Admitted")
	remotePod := executionpod.Build(r)
	remotePod.OwnerReferences = nil
	remotePod.Labels[OriginLabel] = "multikueue"
	remotePod.Annotations[PrebuiltKey] = wl.GetName()
	if err := c.Create(context.Background(), remotePod); err != nil {
		t.Fatal(err)
	}
	remotePod.Status.Phase = corev1.PodRunning
	if err := c.Status().Update(context.Background(), remotePod); err != nil {
		t.Fatal(err)
	}
	return remotePod
}

func TestKueueAdmissionEvictionAndNoRecreation(t *testing.T) {
	b, r := fixture(t)
	ctx := context.Background()
	p, h := createManagerPod(t, b, r)
	w := createWorkload(t, b, r, p)
	o, err := b.Observe(ctx, r, h)
	if err != nil || o.Admission != "Waiting" || o.Reason != "WaitingForAdmission" || o.Stage != "WaitingForAdmission" {
		t.Fatalf("waiting: %+v %v", o, err)
	}
	h = o.Handle
	setCondition(t, b.Client, w, "Admitted")
	o, err = b.Observe(ctx, r, h)
	if err != nil || o.Admission != "Admitted" {
		t.Fatalf("admission: %+v %v", o, err)
	}
	setCondition(t, b.Client, w, "Evicted")
	if err := b.Client.Delete(ctx, p); err != nil {
		t.Fatal(err)
	}
	o, err = b.Observe(ctx, r, h)
	if err != nil || o.Phase != "Unknown" || o.FailureClass != "AdmissionEvicted" || o.Stage != "ObservationUnavailable" {
		t.Fatalf("eviction: %+v %v", o, err)
	}
	if _, err := b.Ensure(ctx, r, h); !errors.Is(err, execution.ErrResourceMissing) {
		t.Fatalf("evicted Pod was recreated: %v", err)
	}
}
func TestMissingWebhookFailsClosed(t *testing.T) {
	b, r := fixture(t)
	u, err := b.Ensure(context.Background(), r, execution.Handle{})
	if err != nil {
		t.Fatal(err)
	}
	o, err := b.Observe(context.Background(), r, u.Handle)
	if err != nil || o.Phase != "Unknown" || o.Reason != "KueueManagementMissing" {
		t.Fatalf("missing webhook: %+v %v", o, err)
	}
}
func TestVolcanoOwnsSinglePodGroupAndCleanup(t *testing.T) {
	b, r := fixture(t)
	r.Configuration = execution.Configuration{Backend: execution.NativeBackend, Scheduler: "volcano", VolcanoQueue: "batch"}
	r.PodSpec.SchedulerName = "volcano"
	createObject(t, b.Client, VolcanoQueueGVK, "", "batch", map[string]any{})
	ctx := context.Background()
	u, err := b.Ensure(ctx, r, execution.Handle{})
	if err != nil || u.Complete || u.Handle.PodGroupUID == "" {
		t.Fatalf("group reservation: %+v %v", u, err)
	}
	u, err = b.Ensure(ctx, r, u.Handle)
	if err != nil || !u.Complete {
		t.Fatalf("Pod creation: %+v %v", u, err)
	}
	p := &corev1.Pod{}
	if err := b.Client.Get(ctx, client.ObjectKey{Namespace: r.Namespace, Name: u.Handle.PodName}, p); err != nil {
		t.Fatal(err)
	}
	if p.Annotations[GroupAnnotation] != p.Name || p.Spec.SchedulerName != "volcano" {
		t.Fatal("missing Volcano membership")
	}
	for range 5 {
		u, err = b.Stop(ctx, r, u.Handle)
		if err != nil {
			t.Fatal(err)
		}
		if u.Complete {
			break
		}
	}
	if !u.Complete {
		t.Fatal("cleanup did not converge")
	}
	if _, err := get(ctx, b.Reader, PodGroupGVK, r.Namespace, p.Name); !apierrors.IsNotFound(err) {
		t.Fatalf("PodGroup leaked: %v", err)
	}
}
func TestVolcanoRejectsForeignGroup(t *testing.T) {
	b, r := fixture(t)
	r.Configuration = execution.Configuration{Backend: execution.NativeBackend, Scheduler: "volcano"}
	r.PodSpec.SchedulerName = "volcano"
	createObject(t, b.Client, VolcanoQueueGVK, "", "default", map[string]any{})
	createObject(t, b.Client, PodGroupGVK, r.Namespace, executionpod.Name(r.Key), map[string]any{"minMember": int64(1)})
	if _, err := b.Ensure(context.Background(), r, execution.Handle{}); !errors.Is(err, execution.ErrIdentityConflict) {
		t.Fatalf("foreign group adopted: %v", err)
	}
}
func TestRemoteObservationsIgnoreManagerSuccessAndRequireWorker(t *testing.T) {
	b, r := fixture(t)
	worker := enableRemote(t, b, &r)
	ctx := context.Background()
	p, h := createManagerPod(t, b, r)
	w := createWorkload(t, b, r, p)
	p.Status.Phase = corev1.PodSucceeded
	if err := b.Client.Status().Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	o, err := b.Observe(ctx, r, h)
	if err != nil || o.Phase != "Unknown" {
		t.Fatalf("trusted manager mirror: %+v %v", o, err)
	}
	rp := createRemote(t, worker, r, p, w)
	o, err = b.Observe(ctx, r, o.Handle)
	if err != nil || o.Phase != "Running" || o.Source != "WorkerPod" || o.Handle.WorkerPodUID != rp.UID {
		t.Fatalf("worker observation: %+v %v", o, err)
	}
	h = o.Handle
	rp.Status.Phase = corev1.PodSucceeded
	if err := worker.Status().Update(ctx, rp); err != nil {
		t.Fatal(err)
	}
	o, err = b.Observe(ctx, r, h)
	if err != nil || o.Phase != "Succeeded" {
		t.Fatalf("worker success: %+v %v", o, err)
	}
	b.Workers["worker"] = failingReader{}
	o, err = b.Observe(ctx, r, h)
	if err != nil || o.Phase != "Unknown" || o.Available || o.Reason != "WorkerUnavailable" {
		t.Fatalf("disconnection: %+v %v", o, err)
	}
}
func TestRemoteStopWaitsForWorkerAbsenceAfterManagerLoss(t *testing.T) {
	b, r := fixture(t)
	worker := enableRemote(t, b, &r)
	ctx := context.Background()
	p, h := createManagerPod(t, b, r)
	w := createWorkload(t, b, r, p)
	rp := createRemote(t, worker, r, p, w)
	o, err := b.Observe(ctx, r, h)
	if err != nil {
		t.Fatal(err)
	}
	h = o.Handle
	// Simulate external loss, including removal of Milos' finalizer.
	p.Finalizers = nil
	if err := b.Client.Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := b.Client.Delete(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := b.Client.Delete(ctx, w); err != nil {
		t.Fatal(err)
	}
	u, err := b.Stop(ctx, r, h)
	if err != nil || u.Complete {
		t.Fatalf("manager absence completed remote stop: %+v %v", u, err)
	}
	b.Workers["worker"] = failingReader{}
	if _, err := b.Stop(ctx, r, h); err == nil {
		t.Fatal("worker disconnection treated as cleanup")
	}
	b.Workers["worker"] = worker
	// Simulate upstream MultiKueue cleanup; Milos itself must not write to the worker.
	if err := worker.Delete(ctx, rp); err != nil {
		t.Fatal(err)
	}
	rw, err := get(ctx, worker, WorkloadGVK, r.Namespace, w.GetName())
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Delete(ctx, rw); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		u, err = b.Stop(ctx, r, h)
		if err != nil || u.Complete {
			break
		}
	}
	if err != nil || !u.Complete {
		t.Fatalf("remote cleanup did not converge: %+v %v", u, err)
	}
}
func TestPolicyMismatchCannotCreate(t *testing.T) {
	b, r := fixture(t)
	enableRemote(t, b, &r)
	r.Configuration.Backend = execution.KueueBackend
	r.Configuration.AdmissionCheck = ""
	r.Configuration.WorkerClusters = nil
	if _, err := b.Ensure(context.Background(), r, execution.Handle{}); err == nil {
		t.Fatal("local backend accepted a MultiKueue queue")
	}
	r.Configuration.Backend = execution.MultiKueueBackend
	r.Configuration.AdmissionCheck = "dispatch"
	r.Configuration.WorkerClusters = []string{"other"}
	if _, err := b.Ensure(context.Background(), r, execution.Handle{}); err == nil {
		t.Fatal("changed worker set was accepted")
	}
}

type failingReader struct{}

func (failingReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return fmt.Errorf("worker unavailable")
}
func (failingReader) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return fmt.Errorf("worker unavailable")
}

func TestRemoteStopRetainsManagerIdentityUntilWorkersDisappear(t *testing.T) {
	b, r := fixture(t)
	worker := enableRemote(t, b, &r)
	ctx := context.Background()
	p, h := createManagerPod(t, b, r)
	w := createWorkload(t, b, r, p)
	rp := createRemote(t, worker, r, p, w)
	o, err := b.Observe(ctx, r, h)
	if err != nil {
		t.Fatal(err)
	}
	h = o.Handle
	u, err := b.Stop(ctx, r, h)
	if err != nil || u.Complete {
		t.Fatalf("premature cleanup: %+v %v", u, err)
	}
	current := &corev1.Pod{}
	if err := b.Client.Get(ctx, client.ObjectKeyFromObject(p), current); err != nil {
		t.Fatal(err)
	}
	if current.DeletionTimestamp.IsZero() || !slices.Contains(current.Finalizers, RemoteCleanupFinalizer) {
		t.Fatal("manager identity not retained during remote cleanup")
	}
	if err := worker.Delete(ctx, rp); err != nil {
		t.Fatal(err)
	}
	rw, err := get(ctx, worker, WorkloadGVK, r.Namespace, w.GetName())
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Delete(ctx, rw); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		u, err = b.Stop(ctx, r, h)
		if err != nil {
			t.Fatal(err)
		}
		h = u.Handle
		if u.Complete {
			break
		}
	}
	if !u.Complete {
		t.Fatal("cleanup did not converge")
	}
}

func TestDisabledIntegrationCannotCreate(t *testing.T) {
	b, r := fixture(t)
	b.EnableKueue = false
	if _, err := b.Ensure(context.Background(), r, execution.Handle{}); err == nil {
		t.Fatal("disabled integration created Pod")
	}
	pods := &corev1.PodList{}
	if err := b.Client.List(context.Background(), pods); err != nil {
		t.Fatal(err)
	}
	if len(pods.Items) != 0 {
		t.Fatal("disabled integration leaked Pod")
	}
}

func TestRemoteCollectUsesPersistedWorkerResultDespiteManagerLag(t *testing.T) {
	b, r := fixture(t)
	worker := enableRemote(t, b, &r)
	ctx := context.Background()
	p, h := createManagerPod(t, b, r)
	w := createWorkload(t, b, r, p)
	rp := createRemote(t, worker, r, p, w)
	rp.Status.Phase = corev1.PodSucceeded
	if err := worker.Status().Update(ctx, rp); err != nil {
		t.Fatal(err)
	}
	o, err := b.Observe(ctx, r, h)
	if err != nil || o.Phase != "Succeeded" {
		t.Fatalf("worker result: %+v %v", o, err)
	}
	h = o.Handle
	ack := execution.ResultAck{Key: r.Key, PodUID: h.PodUID, Phase: "Succeeded"}
	u, err := b.Collect(ctx, r, h, ack)
	if err != nil || u.Complete {
		t.Fatalf("collection must start cleanup despite stale manager phase and wait for worker absence: %+v %v", u, err)
	}
}

func TestRemoteTimingUsesWorkerAndWaitingStage(t *testing.T) {
	b, r := fixture(t)
	worker := enableRemote(t, b, &r)
	ctx := context.Background()
	p, h := createManagerPod(t, b, r)
	w := createWorkload(t, b, r, p)
	managerStart := metav1.NewTime(time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC))
	p.Status.Phase = corev1.PodSucceeded
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{StartedAt: managerStart, FinishedAt: managerStart}}}}
	if err := b.Client.Status().Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	o, err := b.Observe(ctx, r, h)
	if err != nil || o.Stage != "WaitingForWorker" || o.StartedAt != nil || o.FinishedAt != nil || o.Available {
		t.Fatalf("dispatch wait: %+v %v", o, err)
	}
	remotePod := createRemote(t, worker, r, p, w)
	started := metav1.NewTime(time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC))
	remotePod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: started}}}}
	if err := worker.Status().Update(ctx, remotePod); err != nil {
		t.Fatal(err)
	}
	o, err = b.Observe(ctx, r, h)
	if err != nil || o.Source != "WorkerPod" || o.Stage != "Running" || o.StartedAt == nil || !o.StartedAt.Equal(started.Time) {
		t.Fatalf("worker runtime not propagated: %+v %v", o, err)
	}
}
