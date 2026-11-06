//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	milosv1 "github.com/wsq2333/milos/api/v1alpha1"
	"github.com/wsq2333/milos/internal/backends/scheduledpod"
	"github.com/wsq2333/milos/internal/execution"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// TestSchedulingAPIContract uses upstream CRD schemas with two isolated API servers.
// It supplies upstream controller effects explicitly; it does not run Kueue or Volcano.
func TestSchedulingAPIContract(t *testing.T) {
	kueue, volcano := os.Getenv("MILOS_KUEUE_CRDS"), os.Getenv("MILOS_VOLCANO_CRDS")
	if kueue == "" || volcano == "" {
		t.Skip("set MILOS_KUEUE_CRDS and MILOS_VOLCANO_CRDS to upstream CRD directories")
	}
	start := func() client.Client {
		t.Helper()
		existing := false
		e := &envtest.Environment{UseExistingCluster: &existing, CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases"), kueue, volcano}, ErrorIfCRDPathMissing: true, DownloadBinaryAssets: os.Getenv("KUBEBUILDER_ASSETS") == "", DownloadBinaryAssetsVersion: "1.36.0", BinaryAssetsDirectory: filepath.Join(os.TempDir(), "milos-envtest")}
		cfg, err := e.Start()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := e.Stop(); err != nil {
				t.Error(err)
			}
		})
		scheme := runtime.NewScheme()
		if err := corev1.AddToScheme(scheme); err != nil {
			t.Fatal(err)
		}
		if err := milosv1.AddToScheme(scheme); err != nil {
			t.Fatal(err)
		}
		c, err := client.New(cfg, client.Options{Scheme: scheme})
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test"}}); err != nil {
			t.Fatal(err)
		}
		return c
	}
	manager, worker := start(), start()
	ctx := context.Background()
	create := func(c client.Client, gvk schema.GroupVersionKind, ns, name string, spec map[string]any) *unstructured.Unstructured {
		t.Helper()
		u := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
		u.SetGroupVersionKind(gvk)
		u.SetNamespace(ns)
		u.SetName(name)
		if err := c.Create(ctx, u); err != nil {
			t.Fatal(err)
		}
		return u
	}
	create(manager, scheduledpod.VolcanoQueueGVK, "", "batch", map[string]any{"weight": int64(1)})
	create(manager, scheduledpod.ClusterQueueGVK, "", "local", map[string]any{})
	create(manager, scheduledpod.LocalQueueGVK, "test", "local", map[string]any{"clusterQueue": "local"})
	create(manager, scheduledpod.MultiConfigGVK, "", "workers", map[string]any{"clusters": []any{"worker"}})
	create(manager, scheduledpod.AdmissionCheckGVK, "", "dispatch", map[string]any{"controllerName": scheduledpod.MultiController, "parameters": map[string]any{"apiGroup": "kueue.x-k8s.io", "kind": "MultiKueueConfig", "name": "workers"}})
	create(manager, scheduledpod.ClusterQueueGVK, "", "remote", map[string]any{"admissionChecksStrategy": map[string]any{"admissionChecks": []any{map[string]any{"name": "dispatch"}}}})
	create(manager, scheduledpod.LocalQueueGVK, "test", "remote", map[string]any{"clusterQueue": "remote"})
	b := &scheduledpod.Backend{Client: manager, Reader: manager, EnableKueue: true, EnableVolcano: true, EnableMultiKueue: true, Workers: map[string]client.Reader{"worker": worker}, Origin: "multikueue"}
	for _, mode := range []string{"volcano", "kueue", "combined", "multikueue"} {
		t.Run(mode, func(t *testing.T) {
			run := &milosv1.WorkflowRun{ObjectMeta: metav1.ObjectMeta{Name: mode, Namespace: "test"}, Spec: milosv1.WorkflowRunSpec{Definition: milosv1.WorkflowDefinition{Nodes: []milosv1.NodeSpec{{Name: "one", Container: milosv1.ContainerSpec{Image: "test"}}}}}}
			run.Spec.DesiredState = "Running"
			run.Spec.ExecutionPolicy = milosv1.ExecutionPolicy{Parallelism: 1}
			run.Spec.Execution = milosv1.ExecutionConfig{Backend: "native-pod", ContractVersion: "v1", Admission: milosv1.AdmissionConfig{Provider: "None"}, Placement: milosv1.PlacementConfig{Provider: "Local"}, Scheduler: milosv1.SchedulerConfig{Name: "default-scheduler"}}
			if err := manager.Create(ctx, run); err != nil {
				t.Fatal(err)
			}
			r := execution.Request{Key: execution.Key{WorkflowUID: run.UID, NodeID: "one"}, Namespace: "test", WorkflowName: run.Name, OwnerAPIVersion: milosv1.GroupVersion.String(), Digest: "test", Configuration: execution.Configuration{Backend: execution.KueueBackend, Scheduler: "default-scheduler", QueueName: "local"}, PodSpec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, SchedulerName: "default-scheduler", Containers: []corev1.Container{{Name: "main", Image: "test"}}}}
			if mode == "volcano" {
				r.Configuration.Backend = execution.NativeBackend
				r.Configuration.QueueName = ""
			}
			if mode == "volcano" || mode == "combined" {
				r.Configuration.Scheduler = "volcano"
				r.Configuration.VolcanoQueue = "batch"
				r.PodSpec.SchedulerName = "volcano"
			}
			if mode == "multikueue" {
				r.Configuration.Backend = execution.MultiKueueBackend
				r.Configuration.QueueName = "remote"
				r.Configuration.AdmissionCheck = "dispatch"
				r.Configuration.WorkerClusters = []string{"worker"}
			}
			h := execution.Handle{}
			for range 3 {
				u, err := b.Ensure(ctx, r, h)
				if err != nil {
					t.Fatal(err)
				}
				h = u.Handle
				if u.Complete {
					break
				}
			}
			p := &corev1.Pod{}
			if err := manager.Get(ctx, client.ObjectKey{Namespace: "test", Name: h.PodName}, p); err != nil {
				t.Fatal(err)
			}
			var wl *unstructured.Unstructured
			if mode != "volcano" {
				p.Labels[scheduledpod.ManagedLabel] = "true"
				if err := manager.Update(ctx, p); err != nil {
					t.Fatal(err)
				}
				wl = create(manager, scheduledpod.WorkloadGVK, "test", "pod-"+p.Name, map[string]any{"queueName": r.Configuration.QueueName, "podSets": []any{map[string]any{"name": "main", "count": int64(1), "template": map[string]any{"spec": map[string]any{"restartPolicy": "Never", "containers": []any{map[string]any{"name": "main", "image": "test"}}}}}}})
				controller := true
				wl.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: p.Name, UID: p.UID, Controller: &controller}})
				wl.SetLabels(map[string]string{"kueue.x-k8s.io/job-uid": string(p.UID)})
				if err := manager.Update(ctx, wl); err != nil {
					t.Fatal(err)
				}
			}
			var rp *corev1.Pod
			var rw *unstructured.Unstructured
			if mode == "multikueue" {
				rw = create(worker, scheduledpod.WorkloadGVK, "test", wl.GetName(), wl.Object["spec"].(map[string]any))
				rw.SetLabels(map[string]string{scheduledpod.OriginLabel: "multikueue"})
				if err := worker.Update(ctx, rw); err != nil {
					t.Fatal(err)
				}
				rp = p.DeepCopy()
				rp.ResourceVersion = ""
				rp.UID = ""
				rp.CreationTimestamp = metav1.Time{}
				rp.OwnerReferences = nil
				rp.Finalizers = nil
				rp.ManagedFields = nil
				rp.Labels[scheduledpod.OriginLabel] = "multikueue"
				rp.Annotations[scheduledpod.PrebuiltKey] = wl.GetName()
				if err := worker.Create(ctx, rp); err != nil {
					t.Fatal(err)
				}
			}
			o, err := b.Observe(ctx, r, h)
			if err != nil {
				t.Fatal(err)
			}
			h = o.Handle
			if mode == "multikueue" && o.Source != "WorkerPod" {
				t.Fatalf("wrong source: %+v", o)
			}
			if mode != "volcano" && h.WorkloadUID == "" {
				t.Fatal("Workload identity not recovered")
			}
			u, err := b.Stop(ctx, r, h)
			if err != nil {
				t.Fatal(err)
			}
			h = u.Handle
			if mode == "multikueue" {
				if u.Complete {
					t.Fatal("remote resources still exist")
				}
				if err := worker.Delete(ctx, rp); err != nil {
					t.Fatal(err)
				}
				if err := worker.Delete(ctx, rw); err != nil {
					t.Fatal(err)
				}
			}
			for range 10 {
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
		})
	}
}
