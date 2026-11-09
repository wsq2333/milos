package scheduledpod

import (
	"context"
	"fmt"
	"strings"

	"github.com/wsq2333/milos/internal/execution"
	executionpod "github.com/wsq2333/milos/internal/pod"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func prebuilt(p *corev1.Pod) string {
	if s := p.Annotations[PrebuiltKey]; s != "" {
		return s
	}
	return p.Labels[PrebuiltKey]
}
func (b *Backend) verifyRemote(p *corev1.Pod, r execution.Request, h execution.Handle, cluster string) error {
	if p.Name != executionpod.Name(r.Key) || p.Namespace != r.Namespace || p.Annotations[executionpod.IdentityAnnotation] != executionpod.Identity(r.Key) || p.Annotations[executionpod.DigestAnnotation] != r.Digest || p.Labels[OriginLabel] != b.Origin || prebuilt(p) == "" || (h.WorkloadName != "" && prebuilt(p) != h.WorkloadName) {
		return fmt.Errorf("%w: remote Pod in %s", execution.ErrIdentityConflict, cluster)
	}
	if h.WorkerPodUID != "" && (h.WorkerCluster != cluster || h.WorkerPodUID != p.UID) {
		return fmt.Errorf("%w: remote execution changed in %s", execution.ErrIdentityConflict, cluster)
	}
	return nil
}
func (b *Backend) observeRemote(ctx context.Context, r execution.Request, o execution.Observation, managerWL *unstructured.Unstructured) (execution.Observation, error) {
	o.Phase, o.Placement, o.Source, o.Reason = "Unknown", "Dispatching", "WorkerPod", "WaitingForWorker"
	if err := b.validatePolicy(ctx, r); err != nil {
		return o, err
	}
	var selected *corev1.Pod
	var cluster string
	var selectedWL *unstructured.Unstructured
	for _, name := range r.Configuration.WorkerClusters {
		reader := b.Workers[name]
		p := &corev1.Pod{}
		err := reader.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: executionpod.Name(r.Key)}, p)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			o.Available = false
			o.Reason = "WorkerUnavailable"
			return o, nil
		}
		if err := b.verifyRemote(p, r, o.Handle, name); err != nil {
			return o, err
		}
		if selected != nil {
			o.Available = false
			o.Reason = "MultipleRemoteExecutions"
			return o, nil
		}
		w, err := get(ctx, reader, WorkloadGVK, r.Namespace, o.Handle.WorkloadName)
		if apierrors.IsNotFound(err) {
			o.Reason = "WorkerWorkloadMissing"
			return o, nil
		}
		if err != nil {
			o.Available = false
			o.Reason = "WorkerUnavailable"
			return o, nil
		}
		if w.GetLabels()[OriginLabel] != b.Origin || (o.Handle.WorkerWorkloadUID != "" && o.Handle.WorkerWorkloadUID != w.GetUID()) {
			return o, fmt.Errorf("%w: worker Workload", execution.ErrIdentityConflict)
		}
		selected, cluster, selectedWL = p, name, w
	}
	if selected == nil {
		if o.Handle.WorkerPodUID != "" {
			o.Reason = "WorkerPodMissing"
		}
		return o, nil
	}
	h := o.Handle
	h.WorkerCluster, h.WorkerPodUID, h.WorkerWorkloadUID = cluster, selected.UID, selectedWL.GetUID()
	o = executionpod.ObservePod(selected)
	o.Handle = h
	o.Placement = "Remote"
	o.Source = "WorkerPod"
	o.Admission = "Waiting"
	if condition(selectedWL, "Admitted") {
		o.Admission = "Admitted"
	}
	if condition(selectedWL, "Evicted") || condition(managerWL, "Evicted") {
		o.Phase, o.Reason, o.FailureClass = "Unknown", "AdmissionEvicted", "AdmissionEvicted"
	}
	return o, nil
}

// workersClear requires fresh absence on every eligible worker. A manager-side
// delete response and mirrored Pod status are never remote cleanup receipts.
func (b *Backend) workersClear(ctx context.Context, r execution.Request, h execution.Handle) (bool, error) {
	clear := true
	for _, name := range r.Configuration.WorkerClusters {
		reader := b.Workers[name]
		if reader == nil {
			return false, fmt.Errorf("worker reader %q is unavailable", name)
		}
		p := &corev1.Pod{}
		err := reader.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: executionpod.Name(r.Key)}, p)
		if err == nil {
			// During cleanup a replaced remote Pod still blocks completion. Milos does not delete it.
			unbound := h
			unbound.WorkerPodUID = ""
			if err := b.verifyRemote(p, r, unbound, name); err != nil {
				return false, err
			}
			clear = false
		} else if !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("cannot confirm cleanup on worker %s: %w", name, err)
		}
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(WorkloadGVK.GroupVersion().WithKind("WorkloadList"))
		if err := reader.List(ctx, list, client.InNamespace(r.Namespace), client.MatchingLabels{OriginLabel: b.Origin}); err != nil {
			return false, err
		}
		// Kueue v0.20 uses pod-<PodName>-<hash>, optionally truncating the prefix to 57
		// characters. Inspect both prefixes when a crash lost the Workload association.
		prefix := "pod-" + executionpod.Name(r.Key)
		short := prefix
		if len(short) > 57 {
			short = short[:57]
		}
		for _, w := range list.Items {
			if w.GetName() == h.WorkloadName || strings.HasPrefix(w.GetName(), prefix+"-") || strings.HasPrefix(w.GetName(), short+"-") {
				clear = false
			}
		}
	}
	return clear, nil
}
