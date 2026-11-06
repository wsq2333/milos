package scheduledpod

import (
	"context"
	"fmt"

	"github.com/wsq2333/milos/internal/execution"
	executionpod "github.com/wsq2333/milos/internal/pod"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (b *Backend) workload(ctx context.Context, r execution.Request, h execution.Handle) (*unstructured.Unstructured, execution.Handle, error) {
	if h.WorkloadName != "" {
		w, err := get(ctx, b.Reader, WorkloadGVK, r.Namespace, h.WorkloadName)
		if apierrors.IsNotFound(err) {
			return nil, h, nil
		}
		if err != nil {
			return nil, h, err
		}
		if err := verifyWorkload(w, r, h); err != nil {
			return nil, h, err
		}
		h.WorkloadUID = w.GetUID()
		return w, h, nil
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(WorkloadGVK.GroupVersion().WithKind("WorkloadList"))
	options := []client.ListOption{client.InNamespace(r.Namespace)}
	if h.PodUID != "" {
		options = append(options, client.MatchingLabels{"kueue.x-k8s.io/job-uid": string(h.PodUID)})
	}
	if err := b.Reader.List(ctx, list, options...); err != nil {
		return nil, h, err
	}
	var found *unstructured.Unstructured
	for i := range list.Items {
		w := &list.Items[i]
		owner := metav1.GetControllerOf(w)
		if owner == nil || owner.APIVersion != "v1" || owner.Kind != "Pod" || owner.Name != executionpod.Name(r.Key) {
			continue
		}
		if err := verifyWorkload(w, r, h); err != nil {
			return nil, h, err
		}
		if found != nil {
			return nil, h, fmt.Errorf("multiple Workloads for one execution")
		}
		found = w
	}
	if found != nil {
		h.WorkloadName, h.WorkloadUID = found.GetName(), found.GetUID()
		// Recover a Pod UID even when Kueue deleted the Pod before Milos saved its handle.
		h.PodName, h.PodUID = executionpod.Name(r.Key), metav1.GetControllerOf(found).UID
	}
	return found, h, nil
}
func verifyWorkload(w *unstructured.Unstructured, r execution.Request, h execution.Handle) error {
	owner := metav1.GetControllerOf(w)
	if owner == nil || owner.Kind != "Pod" || owner.APIVersion != "v1" || owner.Name != executionpod.Name(r.Key) || owner.UID == "" || (h.PodUID != "" && owner.UID != h.PodUID) || (h.WorkloadUID != "" && h.WorkloadUID != w.GetUID()) || field(w, "spec", "queueName") != r.Configuration.QueueName {
		return fmt.Errorf("%w: Kueue Workload", execution.ErrIdentityConflict)
	}
	return nil
}
