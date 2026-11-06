// Package backends resolves the immutable execution configuration to a lifecycle backend.
package backends

import (
	"context"

	"github.com/wsq2333/milos/internal/backends/nativepod"
	"github.com/wsq2333/milos/internal/backends/scheduledpod"
	"github.com/wsq2333/milos/internal/execution"
)

type Router struct {
	Native    *nativepod.Backend
	Scheduled *scheduledpod.Backend
}

func (r *Router) resolve(req execution.Request) (execution.Backend, error) {
	if err := execution.ValidateConfiguration(req.Configuration); err != nil {
		return nil, err
	}
	if req.Configuration.Backend == execution.NativeBackend && req.Configuration.Scheduler == "default-scheduler" {
		return r.Native, nil
	}
	return r.Scheduled, nil
}
func (r *Router) Ensure(ctx context.Context, q execution.Request, h execution.Handle) (execution.Update, error) {
	b, e := r.resolve(q)
	if e != nil {
		return execution.Update{}, e
	}
	return b.Ensure(ctx, q, h)
}
func (r *Router) Observe(ctx context.Context, q execution.Request, h execution.Handle) (execution.Observation, error) {
	b, e := r.resolve(q)
	if e != nil {
		return execution.Observation{}, e
	}
	return b.Observe(ctx, q, h)
}
func (r *Router) Stop(ctx context.Context, q execution.Request, h execution.Handle) (execution.Update, error) {
	b, e := r.resolve(q)
	if e != nil {
		return execution.Update{}, e
	}
	return b.Stop(ctx, q, h)
}
func (r *Router) Collect(ctx context.Context, q execution.Request, h execution.Handle, a execution.ResultAck) (execution.Update, error) {
	b, e := r.resolve(q)
	if e != nil {
		return execution.Update{}, e
	}
	return b.Collect(ctx, q, h, a)
}
