//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	milosv1 "github.com/wsq2333/milos/api/v1alpha1"
	"github.com/wsq2333/milos/internal/backends/nativepod"
	"github.com/wsq2333/milos/internal/controller"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// TestAPIContract uses an isolated API server and etcd, never the current kubeconfig.
// There is no kubelet: Pod outcomes are supplied explicitly by the test.
func TestAPIContract(t *testing.T) {
	useExisting := false
	environment := &envtest.Environment{
		UseExistingCluster: &useExisting,
		CRDDirectoryPaths:  []string{filepath.Join("..", "..", "config", "crd", "bases")}, ErrorIfCRDPathMissing: true,
		DownloadBinaryAssets: os.Getenv("KUBEBUILDER_ASSETS") == "", DownloadBinaryAssetsVersion: "1.36.0",
		BinaryAssetsDirectory: filepath.Join(os.TempDir(), "milos-envtest"),
	}
	cfg, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Error(err)
		}
	})
	scheme := runtime.NewScheme()
	if err := milosv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "milos-test"}}
	if err := c.Create(ctx, namespace); err != nil {
		t.Fatal(err)
	}

	// Exercise defaults through raw input, where optional parent fields are absent.
	raw := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": milosv1.GroupVersion.String(), "kind": "WorkflowRun",
		"metadata": map[string]any{"name": "defaults", "namespace": namespace.Name},
		"spec":     map[string]any{"definition": map[string]any{"nodes": []any{map[string]any{"name": "one", "container": map[string]any{"image": "busybox:1.37.0"}}}}},
	}}
	if err := c.Create(ctx, raw); err != nil {
		t.Fatal(err)
	}
	defaulted := &milosv1.WorkflowRun{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(raw), defaulted); err != nil {
		t.Fatal(err)
	}
	if defaulted.Spec.Execution.Backend != "native-pod" || defaulted.Spec.ExecutionPolicy.Parallelism != 1 || defaulted.Spec.DesiredState != "Running" {
		t.Fatalf("missing API defaults: %+v", defaulted.Spec)
	}
	defaulted.Spec.DesiredState = "Cancelled"
	if err := c.Update(ctx, defaulted); err != nil {
		t.Fatal(err)
	}
	defaulted.Spec.DesiredState = "Running"
	if err := c.Update(ctx, defaulted); !apierrors.IsInvalid(err) {
		t.Fatalf("cancellation was reversed: %v", err)
	}
	run := &milosv1.WorkflowRun{ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: namespace.Name}, Spec: milosv1.WorkflowRunSpec{
		DesiredState:    "Running",
		Execution:       milosv1.ExecutionConfig{Backend: "native-pod", ContractVersion: "v1", Admission: milosv1.AdmissionConfig{Provider: "None"}, Placement: milosv1.PlacementConfig{Provider: "Local"}, Scheduler: milosv1.SchedulerConfig{Name: "default-scheduler"}},
		ExecutionPolicy: milosv1.ExecutionPolicy{Parallelism: 1},
		Definition:      milosv1.WorkflowDefinition{Nodes: []milosv1.NodeSpec{{Name: "one", Container: milosv1.ContainerSpec{Image: "busybox:1.37.0"}}, {Name: "two", Dependencies: []string{"one"}, Container: milosv1.ContainerSpec{Image: "busybox:1.37.0"}}}},
	}}
	if err := c.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	key := client.ObjectKeyFromObject(run)
	changed := run.DeepCopy()
	changed.Spec.Definition.Nodes[0].Container.Image = "other"
	if err := c.Update(ctx, changed); !apierrors.IsInvalid(err) {
		t.Fatalf("immutable definition was accepted: %v", err)
	}
	reconciler := &controller.WorkflowRunReconciler{Client: c, Reader: c, Backend: &nativepod.Backend{Client: c, Reader: c}}
	for i := 0; i < 40; i++ {
		if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatal(err)
		}
		if err := c.Get(ctx, key, run); err != nil {
			t.Fatal(err)
		}
		if run.Status.Phase == "Succeeded" {
			break
		}
		list := &corev1.PodList{}
		if err := c.List(ctx, list, client.InNamespace(namespace.Name)); err != nil {
			t.Fatal(err)
		}
		for j := range list.Items {
			p := &list.Items[j]
			if p.Status.Phase == corev1.PodSucceeded {
				continue
			}
			p.Status.Phase = corev1.PodSucceeded
			if err := c.Status().Update(ctx, p); err != nil {
				t.Fatal(err)
			}
		}
	}
	if run.Status.Phase != "Succeeded" {
		t.Fatalf("reconcile did not converge: %+v", run.Status)
	}
	// Verify optimistic status writes reject stale resource versions.
	stale := run.DeepCopy()
	before := stale.DeepCopy()
	run.Status.StopReason = "test"
	if err := c.Status().Update(ctx, run); err != nil {
		t.Fatal(err)
	}
	stale.Status.StopReason = "stale"
	if err := c.Status().Patch(ctx, stale, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); !apierrors.IsConflict(err) {
		t.Fatalf("stale status patch was accepted: %v", err)
	}
	t.Run("profile authoring and replay", func(t *testing.T) { testProfileAPI(t, c, namespace.Name) })
	t.Run("node timeouts", func(t *testing.T) { testNodeTimeoutAPI(t, c, namespace.Name) })

}
