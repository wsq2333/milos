// Package nativepod implements local Pod execution with the default scheduler.
package nativepod

import (
	"context"
	"fmt"
	"time"

	"github.com/wsq2333/milos/internal/execution"
	executionpod "github.com/wsq2333/milos/internal/pod"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Backend struct {
	Client client.Client
	// Reader must bypass the cache: absence and UID checks gate creation and deletion.
	Reader client.Reader
}

var _ execution.Backend = (*Backend)(nil)

func (b *Backend) get(ctx context.Context, r execution.Request, h execution.Handle) (*corev1.Pod, error) {
	if h.PodName != "" && h.PodName != executionpod.Name(r.Key) {
		return nil, execution.ErrIdentityConflict
	}
	p := &corev1.Pod{}
	if err := b.Reader.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: executionpod.Name(r.Key)}, p); err != nil {
		return nil, err
	}
	if err := executionpod.Verify(p, r, h); err != nil {
		return nil, err
	}
	return p, nil
}

func handle(p *corev1.Pod) execution.Handle { return execution.Handle{PodName: p.Name, PodUID: p.UID} }

func (b *Backend) Ensure(ctx context.Context, r execution.Request, h execution.Handle) (execution.Update, error) {
	if r.PodSpec.SchedulerName != corev1.DefaultSchedulerName || r.PodSpec.RestartPolicy != corev1.RestartPolicyNever || len(r.PodSpec.Containers) != 1 || len(r.PodSpec.InitContainers) != 0 || len(r.PodSpec.SchedulingGates) != 0 {
		return execution.Update{}, fmt.Errorf("native-pod requires one container, restartPolicy Never, default-scheduler, and no init containers or scheduling gates")
	}
	p, err := b.get(ctx, r, h)
	if apierrors.IsNotFound(err) {
		if h.PodUID != "" {
			return execution.Update{}, execution.ErrResourceMissing
		}
		p = executionpod.Build(r)
		err = b.Client.Create(ctx, p)
		if apierrors.IsAlreadyExists(err) {
			p, err = b.get(ctx, r, h)
		}
	}
	if err != nil {
		return execution.Update{}, err
	}
	return execution.Update{Handle: handle(p), Complete: true}, nil
}

func (b *Backend) Observe(ctx context.Context, r execution.Request, h execution.Handle) (execution.Observation, error) {
	p, err := b.get(ctx, r, h)
	if apierrors.IsNotFound(err) {
		reason := "NotCreated"
		if h.PodUID != "" {
			reason = "ResourceMissing"
		}
		return execution.Observation{Handle: h, Phase: "Unknown", Reason: reason}, nil
	}
	if err != nil {
		return execution.Observation{}, err
	}
	o := execution.Observation{Handle: handle(p), Available: true, Reason: p.Status.Reason}
	switch p.Status.Phase {
	case corev1.PodSucceeded:
		o.Phase = "Succeeded"
	case corev1.PodFailed:
		o.Phase = "Failed"
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
	}
	return o, nil
}

// Stop uses ordinary Kubernetes deletion; completion means API disappearance,
// not proof of process termination on an unreachable node.
func (b *Backend) Stop(ctx context.Context, r execution.Request, h execution.Handle) (execution.Update, error) {
	p, err := b.get(ctx, r, h)
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
	return execution.Update{Handle: handle(p), RequeueAfter: 2 * time.Second}, nil
}

func (b *Backend) Collect(ctx context.Context, r execution.Request, h execution.Handle, ack execution.ResultAck) (execution.Update, error) {
	if ack.Key != r.Key || ack.PodUID == "" || ack.PodUID != h.PodUID || (ack.Phase != "Succeeded" && ack.Phase != "Failed") {
		return execution.Update{}, execution.ErrResultUnconfirmed
	}
	p, err := b.get(ctx, r, h)
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
