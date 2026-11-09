// Package nativepod implements local Pod execution with the default scheduler.
package nativepod

import (
	"context"
	"fmt"

	"github.com/wsq2333/milos/internal/execution"
	executionpod "github.com/wsq2333/milos/internal/pod"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Backend struct {
	Client client.Client
	Reader client.Reader
}

var _ execution.Backend = (*Backend)(nil)

func (b *Backend) lifecycle() *executionpod.Lifecycle {
	return &executionpod.Lifecycle{Client: b.Client, Reader: b.Reader}
}
func (b *Backend) Ensure(ctx context.Context, r execution.Request, h execution.Handle) (execution.Update, error) {
	if r.PodSpec.SchedulerName != corev1.DefaultSchedulerName || r.PodSpec.RestartPolicy != corev1.RestartPolicyNever || len(r.PodSpec.Containers) != 1 || len(r.PodSpec.InitContainers) != 0 || len(r.PodSpec.SchedulingGates) != 0 {
		return execution.Update{}, fmt.Errorf("native-pod requires one container, restartPolicy Never, default-scheduler, and no init containers or scheduling gates")
	}
	return b.lifecycle().Ensure(ctx, r, h, executionpod.Build(r))
}
func (b *Backend) Observe(ctx context.Context, r execution.Request, h execution.Handle) (execution.Observation, error) {
	return b.lifecycle().Observe(ctx, r, h)
}
func (b *Backend) Stop(ctx context.Context, r execution.Request, h execution.Handle) (execution.Update, error) {
	return b.lifecycle().Stop(ctx, r, h)
}
func (b *Backend) Collect(ctx context.Context, r execution.Request, h execution.Handle, a execution.ResultAck) (execution.Update, error) {
	return b.lifecycle().Collect(ctx, r, h, a)
}
