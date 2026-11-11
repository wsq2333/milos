package scheduledpod

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/wsq2333/milos/internal/execution"
	executionpod "github.com/wsq2333/milos/internal/pod"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Backend struct {
	Client                                       client.Client
	Reader                                       client.Reader
	EnableKueue, EnableMultiKueue, EnableVolcano bool
	// Workers are operator-configured uncached clients; this backend never writes to workers.
	Workers map[string]client.Reader
	Origin  string
}

var _ execution.Backend = (*Backend)(nil)

func (b *Backend) lifecycle() *executionpod.Lifecycle {
	return &executionpod.Lifecycle{Client: b.Client, Reader: b.Reader}
}
func queued(r execution.Request) bool { return r.Configuration.Backend != execution.NativeBackend }
func remote(r execution.Request) bool { return r.Configuration.Backend == execution.MultiKueueBackend }

func (b *Backend) Ensure(ctx context.Context, r execution.Request, h execution.Handle) (execution.Update, error) {
	if err := b.validatePolicy(ctx, r); err != nil {
		return execution.Update{}, err
	}
	if r.PodSpec.RestartPolicy != corev1.RestartPolicyNever || len(r.PodSpec.Containers) != 1 || len(r.PodSpec.InitContainers) != 0 || len(r.PodSpec.SchedulingGates) != 0 || r.PodSpec.SchedulerName != r.Configuration.Scheduler {
		return execution.Update{}, fmt.Errorf("unsupported Pod execution contract")
	}
	p := executionpod.Build(r)
	if r.Configuration.Scheduler == "volcano" {
		group, err := get(ctx, b.Reader, PodGroupGVK, r.Namespace, p.Name)
		if apierrors.IsNotFound(err) {
			if h.PodGroupUID != "" {
				return execution.Update{}, execution.ErrResourceMissing
			}
			group = object(PodGroupGVK)
			group.SetName(p.Name)
			group.SetNamespace(p.Namespace)
			group.SetAnnotations(p.Annotations)
			group.SetOwnerReferences(p.OwnerReferences)
			queue := r.Configuration.VolcanoQueue
			if queue == "" {
				queue = "default"
			}
			group.Object["spec"] = map[string]any{"minMember": int64(1), "queue": queue}
			err = b.Client.Create(ctx, group)
			if apierrors.IsAlreadyExists(err) {
				group, err = get(ctx, b.Reader, PodGroupGVK, r.Namespace, p.Name)
			}
		}
		if err != nil {
			return execution.Update{}, err
		}
		if err := verifyGroup(group, r, h); err != nil {
			return execution.Update{}, err
		}
		expectedQueue := r.Configuration.VolcanoQueue
		if expectedQueue == "" {
			expectedQueue = "default"
		}
		members, _, _ := unstructured.NestedInt64(group.Object, "spec", "minMember")
		if !group.GetDeletionTimestamp().IsZero() || members != 1 || field(group, "spec", "queue") != expectedQueue {
			return execution.Update{}, fmt.Errorf("Volcano PodGroup policy changed or is terminating")
		}
		if h.PodGroupUID == "" {
			h.PodGroupUID = group.GetUID()
			return execution.Update{Handle: h, RequeueAfter: time.Millisecond}, nil
		}
		p.Annotations[GroupAnnotation] = p.Name
	}
	if remote(r) {
		p.Finalizers = append(p.Finalizers, RemoteCleanupFinalizer)
	}
	if queued(r) {
		p.Labels[QueueLabel] = r.Configuration.QueueName
		// Gate at creation even if the upstream webhook is unavailable or excludes this namespace.
		// Only Kueue removes this gate; Milos never grants admission.
		p.Spec.SchedulingGates = []corev1.PodSchedulingGate{{Name: AdmissionGate}}
	}
	return b.lifecycle().Ensure(ctx, r, h, p)
}

func (b *Backend) Observe(ctx context.Context, r execution.Request, h execution.Handle) (result execution.Observation, resultErr error) {
	defer func() {
		if result.Phase == "Unknown" {
			result.Stage = "ObservationUnavailable"
			if result.Reason == "WaitingForWorker" {
				result.Stage = "WaitingForWorker"
			}
			if result.Reason == "NotCreated" {
				result.Stage = "Creating"
			}
		}
	}()
	p, err := b.lifecycle().Get(ctx, r, h)
	if err != nil && !apierrors.IsNotFound(err) {
		return execution.Observation{}, err
	}
	if p != nil && err == nil {
		h = executionpod.BindHandle(h, p)
	} else {
		p = nil
	}
	o := execution.Observation{Handle: h, Phase: "Unknown", Reason: "NotCreated", Admission: "NotRequired", Placement: "Local", Source: "LocalPod"}
	if h.PodUID != "" {
		o.Reason = "ResourceMissing"
	}
	if p != nil {
		o = executionpod.ObservePod(p)
		o.Handle = h
	}
	if r.Configuration.Scheduler == "volcano" {
		group, err := get(ctx, b.Reader, PodGroupGVK, r.Namespace, executionpod.Name(r.Key))
		if err != nil && !apierrors.IsNotFound(err) {
			return o, err
		}
		if err == nil {
			if err := verifyGroup(group, r, h); err != nil {
				return o, err
			}
			o.Handle.PodGroupUID = group.GetUID()
		} else if h.PodGroupUID != "" {
			o.Phase, o.Reason = "Unknown", "PodGroupMissing"
		}
	}
	if !queued(r) {
		return o, nil
	}
	wl, h, err := b.workload(ctx, r, o.Handle)
	o.Handle = h
	if err != nil {
		return o, err
	}
	o.Admission = "Waiting"
	if p != nil && (p.Labels[ManagedLabel] != "true" || p.Labels[QueueLabel] != r.Configuration.QueueName) {
		o.Phase, o.Reason = "Unknown", "KueueManagementMissing"
		return o, nil
	}
	if wl == nil {
		o.Phase, o.Reason = "Unknown", "WorkloadMissing"
		return o, nil
	}
	if condition(wl, "Admitted") {
		o.Admission = "Admitted"
	}
	if condition(wl, "Evicted") {
		o.Phase, o.Reason, o.FailureClass = "Unknown", "AdmissionEvicted", "AdmissionEvicted"
		return o, nil
	}
	if remote(r) {
		return b.observeRemote(ctx, r, o, wl)
	}
	if p != nil && o.Phase == "Submitted" && o.Admission == "Waiting" {
		o.Reason = "WaitingForAdmission"
		o.Stage = "WaitingForAdmission"
	}
	return o, nil
}

func (b *Backend) Stop(ctx context.Context, r execution.Request, h execution.Handle) (execution.Update, error) {
	// Resolve the handle before deletion, including creation that succeeded before status was saved.
	p, err := b.lifecycle().Get(ctx, r, h)
	if err != nil && !apierrors.IsNotFound(err) {
		return execution.Update{}, err
	}
	if err == nil {
		h = executionpod.BindHandle(h, p)
	}
	if queued(r) {
		wl, discovered, err := b.workload(ctx, r, h)
		if err != nil {
			return execution.Update{}, err
		}
		if discovered != h {
			return execution.Update{Handle: discovered, RequeueAfter: time.Millisecond}, nil
		}
		if remote(r) {
			if err := b.validatePolicy(ctx, r); err != nil {
				return execution.Update{}, err
			}
		}
		// Mark the Pod terminating before deleting its Workload. This prevents the
		// Kueue Pod controller from creating a replacement Workload during cleanup.
		// Our finalizer retains the manager identity until worker absence is verified.
		if p != nil {
			if err := deleteObject(ctx, b.Client, p); err != nil {
				return execution.Update{}, err
			}
		}
		if wl != nil {
			if err := deleteObject(ctx, b.Client, wl); err != nil {
				return execution.Update{}, err
			}
		}
		if remote(r) {
			clear, err := b.workersClear(ctx, r, h)
			if err != nil {
				return execution.Update{}, err
			}
			if !clear {
				return execution.Update{Handle: h, RequeueAfter: 5 * time.Second}, nil
			}
			current, err := b.lifecycle().Get(ctx, r, h)
			if err != nil && !apierrors.IsNotFound(err) {
				return execution.Update{}, err
			}
			if err == nil && slices.Contains(current.Finalizers, RemoteCleanupFinalizer) {
				before := current.DeepCopy()
				current.Finalizers = slices.DeleteFunc(current.Finalizers, func(f string) bool { return f == RemoteCleanupFinalizer })
				if err := b.Client.Patch(ctx, current, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
					return execution.Update{}, err
				}
				return execution.Update{Handle: h, RequeueAfter: time.Second}, nil
			}
		}
	}
	update, err := b.lifecycle().Stop(ctx, r, h)
	if err != nil {
		return update, err
	}
	if !update.Complete {
		return update, nil
	}
	if queued(r) {
		wl, _, err := b.workload(ctx, r, h)
		if err != nil {
			return execution.Update{}, err
		}
		if wl != nil {
			if err := deleteObject(ctx, b.Client, wl); err != nil {
				return execution.Update{}, err
			}
			return execution.Update{Handle: h, RequeueAfter: 5 * time.Second}, nil
		}
		if remote(r) {
			clear, err := b.workersClear(ctx, r, h)
			if err != nil {
				return execution.Update{}, err
			}
			if !clear {
				return execution.Update{Handle: h, RequeueAfter: 5 * time.Second}, nil
			}
		}
	}
	if r.Configuration.Scheduler == "volcano" {
		g, err := get(ctx, b.Reader, PodGroupGVK, r.Namespace, executionpod.Name(r.Key))
		if err != nil && !apierrors.IsNotFound(err) {
			return execution.Update{}, err
		}
		if err == nil {
			if err := verifyGroup(g, r, h); err != nil {
				return execution.Update{}, err
			}
			h.PodGroupUID = g.GetUID()
			if err := deleteObject(ctx, b.Client, g); err != nil {
				return execution.Update{}, err
			}
			return execution.Update{Handle: h, RequeueAfter: time.Second}, nil
		}
	}
	return execution.Update{Handle: h, Complete: true}, nil
}

func (b *Backend) Collect(ctx context.Context, r execution.Request, h execution.Handle, ack execution.ResultAck) (execution.Update, error) {
	if ack.Key != r.Key || ack.PodUID == "" || ack.PodUID != h.PodUID || (ack.Phase != "Succeeded" && ack.Phase != "Failed") {
		return execution.Update{}, execution.ErrResultUnconfirmed
	}
	if remote(r) {
		// The acknowledged result came from the bound worker Pod. Its manager
		// projection may lag; cleanup still requires fresh worker absence.
		if h.WorkerPodUID == "" {
			return execution.Update{}, execution.ErrResultUnconfirmed
		}
		return b.Stop(ctx, r, h)
	}
	// A persisted result remains valid if its resources were already collected.
	p, err := b.lifecycle().Get(ctx, r, h)
	if err != nil && !apierrors.IsNotFound(err) {
		return execution.Update{}, err
	}
	if err == nil && string(p.Status.Phase) != ack.Phase {
		return execution.Update{}, execution.ErrResultUnconfirmed
	}
	return b.Stop(ctx, r, h)
}
