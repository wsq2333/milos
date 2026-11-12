package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	milosv1 "github.com/wsq2333/milos/api/v1alpha1"
	"github.com/wsq2333/milos/internal/execution"
	"github.com/wsq2333/milos/internal/resources"
	"github.com/wsq2333/milos/internal/workflow"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
)

// prepare validates the supported contract before any backend operation.
func prepare(run *milosv1.WorkflowRun) ([]workflow.Node, string, error) {
	s := &run.Spec
	if s.Execution.ContractVersion != "v1" {
		return nil, "", fmt.Errorf("unsupported execution contract version")
	}
	if err := execution.ValidateConfiguration(configuration(s.Execution)); err != nil {
		return nil, "", err
	}
	expectedAdmission, expectedPlacement := "None", "Local"
	if s.Execution.Backend != execution.NativeBackend {
		expectedAdmission = "Kueue"
	}
	if s.Execution.Backend == execution.MultiKueueBackend {
		expectedPlacement = "MultiKueue"
	}
	if s.Execution.Admission.Provider != expectedAdmission || s.Execution.Placement.Provider != expectedPlacement {
		return nil, "", fmt.Errorf("backend, admission and placement do not agree")
	}

	if s.DesiredState != "Running" && s.DesiredState != "Cancelled" {
		return nil, "", fmt.Errorf("unsupported desiredState")
	}
	if s.ExecutionPolicy.Parallelism < 1 || s.ExecutionPolicy.Parallelism > 128 || s.ExecutionPolicy.MaxRetries != 0 || s.ExecutionPolicy.WorkflowTimeoutSeconds < 0 || s.ExecutionPolicy.WorkflowTimeoutSeconds > 604800 {
		return nil, "", fmt.Errorf("invalid execution policy; parallelism must be 1..128, retries 0, and timeout 0..604800")
	}
	if err := validateTimeouts(s.ExecutionPolicy.NodeTimeouts); err != nil {
		return nil, "", err
	}
	if len(s.Definition.Volumes) > 32 {
		return nil, "", fmt.Errorf("at most 32 volumes are supported")
	}
	volumes := map[string]bool{}
	for _, v := range s.Definition.Volumes {
		if len(validation.IsDNS1123Label(v.Name)) != 0 || volumes[v.Name] {
			return nil, "", fmt.Errorf("invalid or duplicate volume %q", v.Name)
		}
		volumes[v.Name] = true
		source := v.VolumeSource.DeepCopy()
		count := 0
		if source.EmptyDir != nil {
			count++
			source.EmptyDir = nil
		}
		if source.PersistentVolumeClaim != nil {
			count++
			source.PersistentVolumeClaim = nil
		}
		data, _ := json.Marshal(source)
		if count != 1 || string(data) != "{}" {
			return nil, "", fmt.Errorf("volume %q must use exactly one emptyDir or persistentVolumeClaim source", v.Name)
		}
	}
	nodes := make([]workflow.Node, 0, len(s.Definition.Nodes))
	for _, n := range s.Definition.Nodes {
		if err := validateTimeouts(n.Timeouts); err != nil {
			return nil, "", fmt.Errorf("node %q: %w", n.Name, err)
		}
		if len(validation.IsDNS1123Label(n.Name)) != 0 {
			return nil, "", fmt.Errorf("invalid node name %q", n.Name)
		}
		if n.Requirements != nil {
			name := n.Requirements.ProfileRef.Name
			if name == "" || len(validation.IsDNS1123Subdomain(name)) != 0 {
				return nil, "", fmt.Errorf("node %q: invalid requirements.profileRef.name", n.Name)
			}
			if s.Execution.Backend != "native-pod" || s.Execution.Scheduler.Name != "default-scheduler" {
				return nil, "", fmt.Errorf("profiles currently require native-pod/default-scheduler")
			}
		}
		c := n.Container
		if n.Requirements != nil {
			rr := resources.Normalize(c.Resources)
			for name, q := range rr.Requests {
				if len(validation.IsQualifiedName(string(name))) != 0 {
					return nil, "", fmt.Errorf("invalid resource name %q", name)
				}
				if strings.Contains(string(name), "/") {
					lim, ok := rr.Limits[name]
					_, integer := q.AsInt64()
					if !ok || !integer || q.Cmp(lim) != 0 {
						return nil, "", fmt.Errorf("extended resource %q requires equal integer requests and limits", name)
					}
				}
			}
		}

		if c.Image == "" {
			return nil, "", fmt.Errorf("node %q requires an image", n.Name)
		}
		if len(c.Resources.Claims) != 0 {
			return nil, "", fmt.Errorf("node %q: resource claims are not supported", n.Name)
		}
		for name, q := range c.Resources.Requests {
			if q.Sign() < 0 {
				return nil, "", fmt.Errorf("negative resource request on %q", n.Name)
			}
			if limit, ok := c.Resources.Limits[name]; ok && limit.Cmp(q) < 0 {
				return nil, "", fmt.Errorf("resource request exceeds limit on %q", n.Name)
			}
		}
		for _, q := range c.Resources.Limits {
			if q.Sign() < 0 {
				return nil, "", fmt.Errorf("negative resource limit on %q", n.Name)
			}
		}
		for _, mount := range c.VolumeMounts {
			if !volumes[mount.Name] {
				return nil, "", fmt.Errorf("unknown volume %q on node %q", mount.Name, n.Name)
			}
		}
		nodes = append(nodes, workflow.Node{Name: n.Name, Dependencies: n.Dependencies})
	}
	if _, err := workflow.Validate(nodes); err != nil {
		return nil, "", err
	}
	immutable := s.DeepCopy()
	immutable.DesiredState = ""
	sort.Strings(immutable.Execution.Placement.WorkerClusters)
	// Match the order-independent semantics of CRD map and set lists.
	sort.Slice(immutable.Definition.Nodes, func(i, j int) bool { return immutable.Definition.Nodes[i].Name < immutable.Definition.Nodes[j].Name })
	for i := range immutable.Definition.Nodes {
		sort.Strings(immutable.Definition.Nodes[i].Dependencies)
	}
	data, err := json.Marshal(immutable)
	if err != nil {
		return nil, "", err
	}
	if len(data) > 256*1024 {
		return nil, "", fmt.Errorf("execution definition exceeds 256 KiB")
	}
	sum := sha256.Sum256(data)
	return nodes, hex.EncodeToString(sum[:]), nil
}

func request(run *milosv1.WorkflowRun, node milosv1.NodeSpec, status milosv1.NodeStatus) execution.Request {
	c := node.Container
	automount := false
	req := execution.Request{
		Configuration: configuration(run.Spec.Execution),
		Key:           execution.Key{WorkflowUID: run.UID, NodeID: node.Name, Attempt: status.Attempt, DispatchGeneration: status.DispatchGeneration},
		Namespace:     run.Namespace, WorkflowName: run.Name, OwnerAPIVersion: milosv1.GroupVersion.String(), Digest: run.Status.DefinitionDigest,
		PodSpec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever, SchedulerName: run.Spec.Execution.Scheduler.Name,
			AutomountServiceAccountToken: &automount, ServiceAccountName: run.Spec.Definition.ServiceAccountName,
			ImagePullSecrets: run.Spec.Definition.ImagePullSecrets, Volumes: run.Spec.Definition.Volumes,
			Containers: []corev1.Container{{Name: "main", Image: c.Image, Command: c.Command, Args: c.Args, Env: c.Env, Resources: c.Resources, VolumeMounts: c.VolumeMounts, WorkingDir: c.WorkingDir, ImagePullPolicy: c.ImagePullPolicy}},
		},
	}
	if snap, ok := run.Status.ResolvedTasks[node.Name]; ok && node.Requirements != nil {
		req.PodSpec.NodeSelector = snap.Constraints.DeepCopy().NodeSelector
		req.PodSpec.Tolerations = snap.Constraints.DeepCopy().Tolerations
		req.PodSpec.Containers[0].Resources = *snap.Resources.DeepCopy()
		if status.Decision != nil {
			req.Digest = status.Decision.ExecutionDigest
		}
	}
	return req

}

func configuration(c milosv1.ExecutionConfig) execution.Configuration {
	return execution.Configuration{Backend: c.Backend, Scheduler: c.Scheduler.Name, QueueName: c.Admission.QueueRef, VolcanoQueue: c.Scheduler.QueueName, AdmissionCheck: c.Placement.AdmissionCheck, WorkerClusters: c.Placement.WorkerClusters}
}
func executionHandle(s milosv1.NodeStatus) execution.Handle {
	return execution.Handle{PodName: s.PodName, PodUID: types.UID(s.PodUID), WorkloadName: s.WorkloadName, WorkloadUID: types.UID(s.WorkloadUID), PodGroupUID: types.UID(s.PodGroupUID), WorkerCluster: s.WorkerCluster, WorkerPodUID: types.UID(s.WorkerPodUID), WorkerWorkloadUID: types.UID(s.WorkerWorkloadUID)}
}
func applyHandle(s *milosv1.NodeStatus, h execution.Handle) {
	s.PodName, s.PodUID = h.PodName, string(h.PodUID)
	s.WorkloadName, s.WorkloadUID = h.WorkloadName, string(h.WorkloadUID)
	s.PodGroupUID = string(h.PodGroupUID)
	s.WorkerCluster, s.WorkerPodUID, s.WorkerWorkloadUID = h.WorkerCluster, string(h.WorkerPodUID), string(h.WorkerWorkloadUID)
}

func executionDigest(run *milosv1.WorkflowRun, node milosv1.NodeSpec, status milosv1.NodeStatus) string {
	return resources.Digest(struct {
		Definition string
		Resolved   string
		Key        execution.Key
	}{run.Status.DefinitionDigest, run.Status.ResolvedTasks[node.Name].Digest, execution.Key{WorkflowUID: run.UID, NodeID: node.Name, Attempt: status.Attempt, DispatchGeneration: status.DispatchGeneration}})
}

// ValidateDefinition applies the same execution validation used by reconciliation.
func ValidateDefinition(run *milosv1.WorkflowRun) error { _, _, err := prepare(run); return err }
