package nativepod

import (
	"context"
	"errors"
	"testing"

	"github.com/wsq2333/milos/internal/execution"
	executionpod "github.com/wsq2333/milos/internal/pod"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func fixture(t *testing.T) (*Backend, execution.Request) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Pod{}).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			obj.SetUID("pod-uid")
			return c.Create(ctx, obj, opts...)
		},
	}).Build()
	return &Backend{Client: c, Reader: c}, execution.Request{
		Key: execution.Key{WorkflowUID: "workflow-uid", NodeID: "train"}, Namespace: "default", WorkflowName: "test", OwnerAPIVersion: "milos.example.io/v1alpha1", Digest: "digest",
		PodSpec: corev1.PodSpec{SchedulerName: corev1.DefaultSchedulerName, RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "main", Image: "test"}}},
	}
}

func TestEnsureRecoveryAndMissingUID(t *testing.T) {
	b, r := fixture(t)
	ctx := context.Background()
	first, err := b.Ensure(ctx, r, execution.Handle{})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a controller crash after Create but before persisting the handle.
	recovered, err := b.Ensure(ctx, r, execution.Handle{})
	if err != nil || recovered.Handle != first.Handle {
		t.Fatalf("recovery: %+v %v", recovered, err)
	}
	pods := &corev1.PodList{}
	if err := b.Client.List(ctx, pods); err != nil || len(pods.Items) != 1 {
		t.Fatalf("duplicate creation: %v %+v", err, pods.Items)
	}
	if err := b.Client.Delete(ctx, &pods.Items[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Ensure(ctx, r, first.Handle); !errors.Is(err, execution.ErrResourceMissing) {
		t.Fatalf("missing resource was recreated: %v", err)
	}
	o, err := b.Observe(ctx, r, first.Handle)
	if err != nil || o.Available || o.Reason != "ResourceMissing" {
		t.Fatalf("missing observation: %+v %v", o, err)
	}
}

func TestIdentityConflict(t *testing.T) {
	b, r := fixture(t)
	ctx := context.Background()
	p := executionpod.Build(r)
	p.Annotations[executionpod.DigestAnnotation] = "different"
	if err := b.Client.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Ensure(ctx, r, execution.Handle{}); !errors.Is(err, execution.ErrIdentityConflict) {
		t.Fatalf("adopted conflicting Pod: %v", err)
	}
	if _, err := b.Stop(ctx, r, execution.Handle{}); !errors.Is(err, execution.ErrIdentityConflict) {
		t.Fatalf("deleted conflicting Pod: %v", err)
	}
	p.Annotations[executionpod.DigestAnnotation] = r.Digest
	if err := b.Client.Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Stop(ctx, r, execution.Handle{PodName: p.Name, PodUID: "different-uid"}); !errors.Is(err, execution.ErrIdentityConflict) {
		t.Fatalf("ignored UID mismatch: %v", err)
	}
}

func TestCollectRequiresDurableResult(t *testing.T) {
	b, r := fixture(t)
	ctx := context.Background()
	update, err := b.Ensure(ctx, r, execution.Handle{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Collect(ctx, r, update.Handle, execution.ResultAck{}); !errors.Is(err, execution.ErrResultUnconfirmed) {
		t.Fatalf("collected without ack: %v", err)
	}
	ack := execution.ResultAck{Key: r.Key, PodUID: update.Handle.PodUID, Phase: "Succeeded"}
	if _, err := b.Collect(ctx, r, update.Handle, ack); !errors.Is(err, execution.ErrResultUnconfirmed) {
		t.Fatalf("collected live Pod: %v", err)
	}
	p := &corev1.Pod{}
	if err := b.Client.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: update.Handle.PodName}, p); err != nil {
		t.Fatal(err)
	}
	p.Status.Phase = corev1.PodSucceeded
	if err := b.Client.Status().Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Collect(ctx, r, update.Handle, ack); err != nil {
		t.Fatal(err)
	}
	complete, err := b.Collect(ctx, r, update.Handle, ack)
	if err != nil || !complete.Complete {
		t.Fatalf("collection did not converge: %+v %v", complete, err)
	}
}

func TestStopWaitsForDeletion(t *testing.T) {
	b, r := fixture(t)
	ctx := context.Background()
	p := executionpod.Build(r)
	p.Finalizers = []string{"test.example.io/hold"}
	if err := b.Client.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	h := execution.Handle{PodName: p.Name, PodUID: p.UID}
	for range 2 {
		u, err := b.Stop(ctx, r, h)
		if err != nil || u.Complete {
			t.Fatalf("deletion prematurely complete: %+v %v", u, err)
		}
	}
	current := &corev1.Pod{}
	if err := b.Client.Get(ctx, client.ObjectKeyFromObject(p), current); err != nil {
		t.Fatal(err)
	}
	if current.DeletionTimestamp.Equal(&metav1.Time{}) {
		t.Fatal("deletion not requested")
	}
	current.Finalizers = nil
	if err := b.Client.Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	u, err := b.Stop(ctx, r, h)
	if err != nil || !u.Complete {
		t.Fatalf("stop did not converge: %+v %v", u, err)
	}
}
