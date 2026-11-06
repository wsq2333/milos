// Package execution defines the backend lifecycle boundary, not DAG policy.
package execution

import (
	"context"
	"errors"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

const NativeBackend = "native-pod"

var (
	ErrIdentityConflict  = errors.New("execution identity conflict")
	ErrResourceMissing   = errors.New("previously bound execution resource is missing")
	ErrResultUnconfirmed = errors.New("terminal result is not acknowledged")
)

type Key struct {
	WorkflowUID        types.UID
	NodeID             string
	Attempt            int32
	DispatchGeneration int32
}

type Request struct {
	Key             Key
	Namespace       string
	WorkflowName    string
	OwnerAPIVersion string
	Digest          string
	PodSpec         corev1.PodSpec
}

// Handle is persisted by the coordinator; it contains no live API objects.
// Only local Pod identity is needed by the initial backend.
type Handle struct {
	PodName string
	PodUID  types.UID
}

type Observation struct {
	Handle       Handle
	Phase        string
	Reason       string
	Available    bool
	FailureClass string
}

type Update struct {
	Handle       Handle
	Complete     bool
	RequeueAfter time.Duration
}

// ResultAck is constructed only after the result has been committed to status.
type ResultAck struct {
	Key    Key
	PodUID types.UID
	Phase  string
}

type Backend interface {
	Ensure(context.Context, Request, Handle) (Update, error)
	Observe(context.Context, Request, Handle) (Observation, error)
	Stop(context.Context, Request, Handle) (Update, error)
	Collect(context.Context, Request, Handle, ResultAck) (Update, error)
}
