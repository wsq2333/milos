package pod

import (
	"context"
	"time"

	"github.com/wsq2333/milos/internal/execution"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Lifecycle struct {
	Client client.Client
	// Reader must bypass the cache: absence and UID checks gate creation and deletion.
	Reader client.Reader
}

func (b *Lifecycle) Get(ctx context.Context, r execution.Request, h execution.Handle) (*corev1.Pod, error) {
	if h.PodName != "" && h.PodName != Name(r.Key) {
		return nil, execution.ErrIdentityConflict
	}
	p := &corev1.Pod{}
	if err := b.Reader.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: Name(r.Key)}, p); err != nil {
		return nil, err
	}
	if err := Verify(p, r, h); err != nil {
		return nil, err
	}
	return p, nil
}

func BindHandle(h execution.Handle, p *corev1.Pod) execution.Handle {
	h.PodName, h.PodUID = p.Name, p.UID
	return h
}

func (b *Lifecycle) Ensure(ctx context.Context, r execution.Request, h execution.Handle, desired *corev1.Pod) (execution.Update, error) {
	p, err := b.Get(ctx, r, h)
	if apierrors.IsNotFound(err) {
		if h.PodUID != "" {
			return execution.Update{}, execution.ErrResourceMissing
		}
		p = desired.DeepCopy()
		err = b.Client.Create(ctx, p)
		if apierrors.IsAlreadyExists(err) {
			p, err = b.Get(ctx, r, h)
		}
	}
	if err != nil {
		return execution.Update{}, err
	}
	return execution.Update{Handle: BindHandle(h, p), Complete: true}, nil
}

func (b *Lifecycle) Observe(ctx context.Context, r execution.Request, h execution.Handle) (execution.Observation, error) {
	p, err := b.Get(ctx, r, h)
	if apierrors.IsNotFound(err) {
		reason := "NotCreated"
		if h.PodUID != "" {
			reason = "ResourceMissing"
		}
		stage := "ObservationUnavailable"
		if reason == "NotCreated" {
			stage = "Creating"
		}
		return execution.Observation{Handle: h, Phase: "Unknown", Reason: reason, Stage: stage}, nil
	}
	if err != nil {
		return execution.Observation{}, err
	}
	o := ObservePod(p)
	o.Handle = BindHandle(h, p)
	return o, nil
}

func ObservePod(p *corev1.Pod) execution.Observation {
	o := execution.Observation{Available: true, Reason: p.Status.Reason, Message: p.Status.Message, Admission: "NotRequired", Placement: "Local", Source: "LocalPod"}
	for _, c := range p.Status.ContainerStatuses {
		if c.Name != "main" {
			continue
		}
		if c.State.Running != nil && !c.State.Running.StartedAt.IsZero() {
			t := c.State.Running.StartedAt.Time
			o.StartedAt = &t
		}
		if c.State.Terminated != nil {
			if !c.State.Terminated.StartedAt.IsZero() {
				t := c.State.Terminated.StartedAt.Time
				o.StartedAt = &t
			}
			if !c.State.Terminated.FinishedAt.IsZero() {
				t := c.State.Terminated.FinishedAt.Time
				o.FinishedAt = &t
			}
		}
		if c.State.Waiting != nil {
			o.Reason, o.Message = c.State.Waiting.Reason, c.State.Waiting.Message
		}
	}
	o.Stage = "WaitingForStartup"
	if o.StartedAt != nil {
		o.Stage = "Running"
	}
	if p.Spec.NodeName == "" && o.StartedAt == nil {
		o.Stage = "WaitingForScheduling"
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodScheduled && c.Status != corev1.ConditionTrue {
				o.Reason, o.Message = c.Reason, c.Message
			}
		}
		if len(p.Spec.SchedulingGates) > 0 {
			o.Stage = "WaitingForAdmission"
			if o.Reason == "" {
				o.Reason = "SchedulingGated"
			}
		}
	}
	switch p.Status.Phase {
	case corev1.PodSucceeded:
		o.Phase = "Succeeded"
		o.Stage = "Finished"
	case corev1.PodFailed:
		o.Phase = "Failed"
		o.Stage = "Finished"
		o.FailureClass = "Infrastructure"
		for _, c := range p.Status.ContainerStatuses {
			if c.Name == "main" && c.State.Terminated != nil && c.State.Terminated.ExitCode != 0 && p.Status.Reason != "Evicted" {
				o.FailureClass = "Application"
			}
		}
	case corev1.PodRunning:
		o.Phase = "Running"
	case corev1.PodPending, "":
		o.Phase = "Submitted"
	default:
		o.Phase = "Unknown"
		o.Stage = "ObservationUnavailable"
	}
	return o
}

// Stop uses ordinary Kubernetes deletion; completion means API disappearance,
// not proof of process termination on an unreachable node.
func (b *Lifecycle) Stop(ctx context.Context, r execution.Request, h execution.Handle) (execution.Update, error) {
	p, err := b.Get(ctx, r, h)
	if apierrors.IsNotFound(err) {
		return execution.Update{Handle: h, Complete: true}, nil
	}
	if err != nil {
		return execution.Update{}, err
	}
	if p.DeletionTimestamp.IsZero() {
		uid := p.UID
		err = b.Client.Delete(ctx, p, client.Preconditions{UID: &uid})
		if err != nil && !apierrors.IsNotFound(err) {
			return execution.Update{}, err
		}
	}
	return execution.Update{Handle: BindHandle(h, p), RequeueAfter: 2 * time.Second}, nil
}

func (b *Lifecycle) Collect(ctx context.Context, r execution.Request, h execution.Handle, ack execution.ResultAck) (execution.Update, error) {
	if ack.Key != r.Key || ack.PodUID == "" || ack.PodUID != h.PodUID || (ack.Phase != "Succeeded" && ack.Phase != "Failed") {
		return execution.Update{}, execution.ErrResultUnconfirmed
	}
	p, err := b.Get(ctx, r, h)
	if apierrors.IsNotFound(err) {
		return execution.Update{Handle: h, Complete: true}, nil
	}
	if err != nil {
		return execution.Update{}, err
	}
	if string(p.Status.Phase) != ack.Phase {
		return execution.Update{}, execution.ErrResultUnconfirmed
	}
	return b.Stop(ctx, r, h)
}
