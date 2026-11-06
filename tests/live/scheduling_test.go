//go:build live

// Package live runs opt-in tests against an explicitly selected development cluster.
package live

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	milosv1 "github.com/wsq2333/milos/api/v1alpha1"
	"github.com/wsq2333/milos/internal/backends"
	"github.com/wsq2333/milos/internal/backends/nativepod"
	"github.com/wsq2333/milos/internal/backends/scheduledpod"
	"github.com/wsq2333/milos/internal/controller"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestScheduling creates its own namespace, queues and runs. It never updates
// existing cluster configuration. The empty namespace is retained for inspection.
// Install the Milos CRD separately before opting into this test.
func TestScheduling(t *testing.T) {
	path, name, image := os.Getenv("MILOS_TEST_KUBECONFIG"), os.Getenv("MILOS_TEST_CONTEXT"), os.Getenv("MILOS_TEST_IMAGE")
	if path == "" || name == "" || image == "" {
		t.Skip("set MILOS_TEST_KUBECONFIG, MILOS_TEST_CONTEXT and MILOS_TEST_IMAGE")
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: path}, &clientcmd.ConfigOverrides{CurrentContext: name}).ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Timeout = 15 * time.Second
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
	ctx := context.Background()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "milos-integration-", Labels: map[string]string{"milos.example.io/test": "scheduling"}}}
	if err := c.Create(ctx, ns); err != nil {
		t.Fatal(err)
	}
	t.Logf("context=%s namespace=%s", name, ns.Name)
	eventually(t, time.Minute, func() (bool, error) {
		err := c.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: "default"}, &corev1.ServiceAccount{})
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return err == nil, err
	})
	makeObject := func(kind, namespace, name string, spec map[string]any) *unstructured.Unstructured {
		t.Helper()
		u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "kueue.x-k8s.io/v1beta2", "kind": kind, "spec": spec}}
		u.SetNamespace(namespace)
		u.SetName(name)
		if err := c.Create(ctx, u); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			uid := u.GetUID()
			if err := client.IgnoreNotFound(c.Delete(ctx, u, client.Preconditions{UID: &uid})); err != nil {
				t.Error(err)
			}
		})
		return u
	}
	makeObject("ResourceFlavor", "", ns.Name, map[string]any{})
	for _, queue := range []string{"ready", "blocked"} {
		cpu, mem := "500m", "256Mi"
		if queue == "blocked" {
			cpu, mem = "0", "0"
		}
		makeObject("ClusterQueue", "", ns.Name+"-"+queue, map[string]any{
			"namespaceSelector": map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": ns.Name}},
			"resourceGroups":    []any{map[string]any{"coveredResources": []any{"cpu", "memory"}, "flavors": []any{map[string]any{"name": ns.Name, "resources": []any{map[string]any{"name": "cpu", "nominalQuota": cpu}, map[string]any{"name": "memory", "nominalQuota": mem}}}}}},
		})
		makeObject("LocalQueue", ns.Name, queue, map[string]any{"clusterQueue": ns.Name + "-" + queue})
	}
	backend := &backends.Router{Native: &nativepod.Backend{Client: c, Reader: c}, Scheduled: &scheduledpod.Backend{Client: c, Reader: c, EnableKueue: true}}
	reconciler := &controller.WorkflowRunReconciler{Client: c, Reader: c, Backend: backend}
	for _, mode := range []string{"native", "kueue", "cancel-waiting", "cancel-running"} {
		t.Run(mode, func(t *testing.T) {
			queue := "ready"
			if mode == "cancel-waiting" {
				queue = "blocked"
			}
			command := "echo milos-smoke"
			if mode == "cancel-running" {
				command = "sleep 600"
			}
			run := &milosv1.WorkflowRun{ObjectMeta: metav1.ObjectMeta{Name: mode, Namespace: ns.Name}, Spec: milosv1.WorkflowRunSpec{
				DesiredState: "Running", ExecutionPolicy: milosv1.ExecutionPolicy{Parallelism: 1, WorkflowTimeoutSeconds: 300},
				Execution:  milosv1.ExecutionConfig{Backend: "kueue-pod", ContractVersion: "v1", Admission: milosv1.AdmissionConfig{Provider: "Kueue", QueueRef: queue}, Placement: milosv1.PlacementConfig{Provider: "Local"}, Scheduler: milosv1.SchedulerConfig{Name: "default-scheduler"}},
				Definition: milosv1.WorkflowDefinition{Nodes: []milosv1.NodeSpec{{Name: "one", Container: milosv1.ContainerSpec{Image: image, Command: []string{"sh", "-c", command}, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("32Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("64Mi")}}}}}},
			}}
			if mode == "native" {
				run.Spec.Execution.Backend = "native-pod"
				run.Spec.Execution.Admission = milosv1.AdmissionConfig{Provider: "None"}
			}
			if err := c.Create(ctx, run); err != nil {
				t.Fatal(err)
			}
			key := client.ObjectKeyFromObject(run)
			reconcile := func() error { _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key}); return err }
			t.Cleanup(func() {
				uid := run.UID
				if err := client.IgnoreNotFound(c.Delete(ctx, run, client.Preconditions{UID: &uid})); err != nil {
					t.Error(err)
					return
				}
				eventually(t, 2*time.Minute, func() (bool, error) {
					if err := reconcile(); err != nil {
						return false, err
					}
					err := c.Get(ctx, key, run)
					return apierrors.IsNotFound(err), client.IgnoreNotFound(err)
				})
				pods := &corev1.PodList{}
				if err := c.List(ctx, pods, client.InNamespace(ns.Name)); err != nil {
					t.Error(err)
				} else if len(pods.Items) != 0 {
					t.Errorf("cleanup left %d Pods", len(pods.Items))
				}
			})
			cancelled := false
			eventually(t, 4*time.Minute, func() (bool, error) {
				if err := reconcile(); err != nil {
					return false, err
				}
				if err := c.Get(ctx, key, run); err != nil {
					return false, err
				}
				s := run.Status.Nodes["one"]
				if !cancelled && ((mode == "cancel-waiting" && s.WorkloadUID != "" && s.Admission == "Waiting") || (mode == "cancel-running" && s.Phase == "Running")) {
					run.Spec.DesiredState = "Cancelled"
					if err := c.Update(ctx, run); err != nil {
						return false, err
					}
					cancelled = true
				}
				if run.Status.Phase == "Failed" {
					return false, fmt.Errorf("run failed: %+v", run.Status)
				}
				if cancelled {
					return run.Status.Phase == "Cancelled", nil
				}
				return run.Status.Phase == "Succeeded", nil
			})
			t.Logf("phase=%s node=%+v", run.Status.Phase, run.Status.Nodes["one"])
		})
	}
}
func eventually(t *testing.T, timeout time.Duration, f func() (bool, error)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ok, err := f()
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatal("timed out waiting for live cluster convergence")
}
