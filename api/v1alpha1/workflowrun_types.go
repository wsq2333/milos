package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WorkflowRun is an immutable static DAG and its durable execution record.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=mwr
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type WorkflowRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              WorkflowRunSpec   `json:"spec"`
	Status            WorkflowRunStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type WorkflowRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WorkflowRun `json:"items"`
}

// WorkflowRunSpec separates immutable execution inputs from cancellation intent.
// +kubebuilder:validation:XValidation:rule="self.definition == oldSelf.definition",message="definition is immutable"
// +kubebuilder:validation:XValidation:rule="self.execution == oldSelf.execution",message="execution is immutable"
// +kubebuilder:validation:XValidation:rule="self.executionPolicy == oldSelf.executionPolicy",message="executionPolicy is immutable"
// +kubebuilder:validation:XValidation:rule="oldSelf.desiredState != 'Cancelled' || self.desiredState == 'Cancelled'",message="cancellation cannot be reversed"
type WorkflowRunSpec struct {
	Definition WorkflowDefinition `json:"definition"`
	// +kubebuilder:default={backend:native-pod,contractVersion:v1,admission:{provider:None},placement:{provider:Local},scheduler:{name:default-scheduler}}
	Execution ExecutionConfig `json:"execution"`
	// +kubebuilder:default={parallelism:1,maxRetries:0,workflowTimeoutSeconds:0}
	ExecutionPolicy ExecutionPolicy `json:"executionPolicy"`
	// +kubebuilder:default=Running
	// +kubebuilder:validation:Enum=Running;Cancelled
	DesiredState string `json:"desiredState"`
}

type WorkflowDefinition struct {
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=128
	// +listType=map
	// +listMapKey=name
	Nodes []NodeSpec `json:"nodes"`
	// +optional
	// +kubebuilder:validation:MaxItems=32
	Volumes []corev1.Volume `json:"volumes,omitempty"`
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
}

type NodeSpec struct {
	// +optional
	Requirements *TaskRequirements `json:"requirements,omitempty"`
	// Timeouts overrides workflow node defaults field by field. Zero disables a limit.
	// +optional
	Timeouts *NodeTimeouts `json:"timeouts,omitempty"`
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`
	// +optional
	// +kubebuilder:validation:MaxItems=128
	// +listType=set
	Dependencies []string      `json:"dependencies,omitempty"`
	Container    ContainerSpec `json:"container"`
}

// ContainerSpec deliberately exposes a restricted Pod input surface.
type ContainerSpec struct {
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`
	// +optional
	Command []string `json:"command,omitempty"`
	// +optional
	Args []string `json:"args,omitempty"`
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`
	// +optional
	// +kubebuilder:default={}
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
	// +optional
	VolumeMounts []corev1.VolumeMount `json:"volumeMounts,omitempty"`
	// +optional
	WorkingDir string `json:"workingDir,omitempty"`
	// +optional
	// +kubebuilder:validation:Enum=Always;IfNotPresent;Never
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`
}

type ExecutionConfig struct {
	// +kubebuilder:validation:Enum=native-pod;kueue-pod;multikueue-pod
	Backend string `json:"backend"`
	// +kubebuilder:validation:Enum=v1
	ContractVersion string          `json:"contractVersion"`
	Admission       AdmissionConfig `json:"admission"`
	Placement       PlacementConfig `json:"placement"`
	Scheduler       SchedulerConfig `json:"scheduler"`
}

type AdmissionConfig struct {
	// QueueRef names a LocalQueue in the WorkflowRun namespace.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	QueueRef string `json:"queueRef,omitempty"`
	// +kubebuilder:validation:Enum=None;Kueue
	Provider string `json:"provider"`
}

type PlacementConfig struct {
	// AdmissionCheck names the platform-managed MultiKueue admission check.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	AdmissionCheck string `json:"admissionCheck,omitempty"`
	// WorkerClusters snapshots all eligible MultiKueueCluster names.
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=16
	WorkerClusters []string `json:"workerClusters,omitempty"`
	// +kubebuilder:validation:Enum=Local;MultiKueue
	Provider string `json:"provider"`
}

type SchedulerConfig struct {
	// QueueName selects a Volcano Queue; it is separate from the Kueue LocalQueue.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	QueueName string `json:"queueName,omitempty"`
	// +kubebuilder:validation:Enum=default-scheduler;volcano
	Name string `json:"name"`
}

type ExecutionPolicy struct {
	// NodeTimeouts supplies defaults for each node; omitted limits are disabled.
	// +optional
	NodeTimeouts *NodeTimeouts `json:"nodeTimeouts,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=128
	// +kubebuilder:default=1
	Parallelism int32 `json:"parallelism"`
	// MaxRetries is reserved; nonzero values are not implemented.
	// +kubebuilder:validation:Enum=0
	// +kubebuilder:default=0
	MaxRetries int32 `json:"maxRetries"`
	// Zero disables the workflow deadline.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=604800
	// +kubebuilder:default=0
	WorkflowTimeoutSeconds int64 `json:"workflowTimeoutSeconds"`
}

// NodeTimeouts contains elapsed wall-clock budgets, not CPU usage limits.
type NodeTimeouts struct {
	// TotalSeconds starts when execution intent is persisted, before resource creation.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=604800
	TotalSeconds *int64 `json:"totalSeconds,omitempty"`
	// RunSeconds starts at the main container's reported startedAt timestamp.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=604800
	RunSeconds *int64 `json:"runSeconds,omitempty"`
}

type WorkflowRunStatus struct {
	// +optional
	ResolvedTasks      map[string]ResolvedTask `json:"resolvedTasks,omitempty"`
	ObservedGeneration int64                   `json:"observedGeneration,omitempty"`
	DefinitionDigest   string                  `json:"definitionDigest,omitempty"`
	Phase              string                  `json:"phase,omitempty"`
	StopReason         string                  `json:"stopReason,omitempty"`
	Nodes              map[string]NodeStatus   `json:"nodes,omitempty"`
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type NodeStatus struct {
	// +optional
	Decision *ExecutionDecision `json:"decision,omitempty"`
	// IntentRecordedAt is committed before any backend creation and never reset.
	IntentRecordedAt *metav1.Time `json:"intentRecordedAt,omitempty"`
	// StartedAt comes from the identity-verified main container, including remote execution.
	StartedAt  *metav1.Time `json:"startedAt,omitempty"`
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
	// Stage describes execution progress independently of the planner phase.
	Stage string `json:"stage,omitempty"`
	// StageSince is the first persisted local observation of the current stage.
	StageSince *metav1.Time `json:"stageSince,omitempty"`
	// Message contains bounded diagnostic context, not logs.
	// +kubebuilder:validation:MaxLength=1024
	Message                 string       `json:"message,omitempty"`
	StopReason              string       `json:"stopReason,omitempty"`
	StopRequestedAt         *metav1.Time `json:"stopRequestedAt,omitempty"`
	WorkloadName            string       `json:"workloadName,omitempty"`
	WorkloadUID             string       `json:"workloadUID,omitempty"`
	PodGroupUID             string       `json:"podGroupUID,omitempty"`
	WorkerCluster           string       `json:"workerCluster,omitempty"`
	WorkerPodUID            string       `json:"workerPodUID,omitempty"`
	WorkerWorkloadUID       string       `json:"workerWorkloadUID,omitempty"`
	Admission               string       `json:"admission,omitempty"`
	Placement               string       `json:"placement,omitempty"`
	ObservationSource       string       `json:"observationSource,omitempty"`
	FailureClass            string       `json:"failureClass,omitempty"`
	Phase                   string       `json:"phase"`
	Attempt                 int32        `json:"attempt"`
	DispatchGeneration      int32        `json:"dispatchGeneration"`
	ExecutionIntentRecorded bool         `json:"executionIntentRecorded,omitempty"`
	PodName                 string       `json:"podName,omitempty"`
	PodUID                  string       `json:"podUID,omitempty"`
	Reason                  string       `json:"reason,omitempty"`
	CleanupComplete         bool         `json:"cleanupComplete,omitempty"`
}
