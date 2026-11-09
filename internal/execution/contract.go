// Package execution defines the backend lifecycle boundary, not DAG policy.
package execution

import (
	"context"
	"errors"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	NativeBackend     = "native-pod"
	KueueBackend      = "kueue-pod"
	MultiKueueBackend = "multikueue-pod"
)

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

type Configuration struct {
	Backend        string
	Scheduler      string
	QueueName      string
	VolcanoQueue   string
	AdmissionCheck string
	WorkerClusters []string
}

type Request struct {
	Configuration   Configuration
	Key             Key
	Namespace       string
	WorkflowName    string
	OwnerAPIVersion string
	Digest          string
	PodSpec         corev1.PodSpec
}

// Handle is persisted by the coordinator; it contains no live API objects.
// Optional fields bind admission, scheduling and remote execution resources.
type Handle struct {
	WorkloadName      string
	WorkloadUID       types.UID
	PodGroupUID       types.UID
	WorkerCluster     string
	WorkerPodUID      types.UID
	WorkerWorkloadUID types.UID
	PodName           string
	PodUID            types.UID
}

type Observation struct {
	Admission    string
	Placement    string
	Source       string
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
