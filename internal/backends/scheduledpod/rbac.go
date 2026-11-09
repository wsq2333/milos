package scheduledpod

// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;delete,roleName=milos-kueue
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=localqueues,verbs=get,roleName=milos-kueue
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=clusterqueues;admissionchecks;multikueueconfigs,verbs=get,roleName=milos-kueue-policy
// +kubebuilder:rbac:groups="",resources=pods,verbs=patch,roleName=milos-multikueue
// +kubebuilder:rbac:groups=scheduling.volcano.sh,resources=podgroups,verbs=get;create;delete,roleName=milos-volcano
// +kubebuilder:rbac:groups=scheduling.volcano.sh,resources=queues,verbs=get,roleName=milos-volcano-policy
