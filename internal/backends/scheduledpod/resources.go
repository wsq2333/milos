// Package scheduledpod integrates Pod execution with optional admission and scheduling APIs.
package scheduledpod

import (
	"context"
	"fmt"

	"github.com/wsq2333/milos/internal/execution"
	executionpod "github.com/wsq2333/milos/internal/pod"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	RemoteCleanupFinalizer = "milos.example.io/remote-cleanup"
	QueueLabel             = "kueue.x-k8s.io/queue-name"
	ManagedLabel           = "kueue.x-k8s.io/managed"
	AdmissionGate          = "kueue.x-k8s.io/admission"
	OriginLabel            = "kueue.x-k8s.io/multikueue-origin"
	PrebuiltKey            = "kueue.x-k8s.io/prebuilt-workload-name"
	GroupAnnotation        = "scheduling.k8s.io/group-name"
	MultiController        = "kueue.x-k8s.io/multikueue"
)

// External APIs are accessed structurally so optional integrations do not pull in
// upstream controller binaries or force their Go/Kubernetes dependency versions.
var (
	WorkloadGVK       = schema.GroupVersionKind{Group: "kueue.x-k8s.io", Version: "v1beta2", Kind: "Workload"}
	LocalQueueGVK     = WorkloadGVK.GroupVersion().WithKind("LocalQueue")
	ClusterQueueGVK   = WorkloadGVK.GroupVersion().WithKind("ClusterQueue")
	AdmissionCheckGVK = WorkloadGVK.GroupVersion().WithKind("AdmissionCheck")
	MultiConfigGVK    = WorkloadGVK.GroupVersion().WithKind("MultiKueueConfig")
	PodGroupGVK       = schema.GroupVersionKind{Group: "scheduling.volcano.sh", Version: "v1beta1", Kind: "PodGroup"}
	VolcanoQueueGVK   = PodGroupGVK.GroupVersion().WithKind("Queue")
)

func object(gvk schema.GroupVersionKind) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	return u
}
func get(ctx context.Context, reader client.Reader, gvk schema.GroupVersionKind, namespace, name string) (*unstructured.Unstructured, error) {
	u := object(gvk)
	err := reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, u)
	return u, err
}
func field(u *unstructured.Unstructured, path ...string) string {
	v, _, _ := unstructured.NestedString(u.Object, path...)
	return v
}
func condition(u *unstructured.Unstructured, name string) bool {
	values, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, v := range values {
		c, ok := v.(map[string]any)
		if ok && c["type"] == name && c["status"] == "True" {
			return true
		}
	}
	return false
}
func deleteObject(ctx context.Context, c client.Client, u client.Object) error {
	if !u.GetDeletionTimestamp().IsZero() {
		return nil
	}
	uid := u.GetUID()
	return client.IgnoreNotFound(c.Delete(ctx, u, client.Preconditions{UID: &uid}))
}
func verifyGroup(g *unstructured.Unstructured, r execution.Request, h execution.Handle) error {
	owner := metav1.GetControllerOf(g)
	if g.GetName() != executionpod.Name(r.Key) || g.GetNamespace() != r.Namespace || owner == nil || owner.UID != r.Key.WorkflowUID || owner.Kind != "WorkflowRun" || owner.Name != r.WorkflowName || owner.APIVersion != r.OwnerAPIVersion || g.GetAnnotations()[executionpod.IdentityAnnotation] != executionpod.Identity(r.Key) || g.GetAnnotations()[executionpod.DigestAnnotation] != r.Digest || (h.PodGroupUID != "" && h.PodGroupUID != g.GetUID()) {
		return fmt.Errorf("%w: Volcano PodGroup", execution.ErrIdentityConflict)
	}
	return nil
}
