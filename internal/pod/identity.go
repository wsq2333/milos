// Package pod builds and verifies execution objects shared by local Pod backends.
package pod

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/wsq2333/milos/internal/execution"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const IdentityAnnotation = "milos.example.io/execution-identity"
const DigestAnnotation = "milos.example.io/definition-digest"

func Identity(key execution.Key) string {
	data, _ := json.Marshal(key)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func Name(key execution.Key) string { return "milos-" + Identity(key)[:48] }

func Build(r execution.Request) *corev1.Pod {
	controller, block := true, true
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: Name(r.Key), Namespace: r.Namespace,
			Labels:          map[string]string{"app.kubernetes.io/managed-by": "milos", "milos.example.io/workflow-uid": string(r.Key.WorkflowUID)},
			Annotations:     map[string]string{IdentityAnnotation: Identity(r.Key), DigestAnnotation: r.Digest},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: r.OwnerAPIVersion, Kind: "WorkflowRun", Name: r.WorkflowName, UID: r.Key.WorkflowUID, Controller: &controller, BlockOwnerDeletion: &block}},
		},
		Spec: *r.PodSpec.DeepCopy(),
	}
}

// Verify refuses adoption across executions, including replacement Pods with the same name.
func Verify(p *corev1.Pod, r execution.Request, h execution.Handle) error {
	owner := metav1.GetControllerOf(p)
	if p.Name != Name(r.Key) || p.Namespace != r.Namespace ||
		p.Annotations[IdentityAnnotation] != Identity(r.Key) || p.Annotations[DigestAnnotation] != r.Digest ||
		owner == nil || owner.UID != r.Key.WorkflowUID || owner.Name != r.WorkflowName || owner.Kind != "WorkflowRun" || owner.APIVersion != r.OwnerAPIVersion ||
		(h.PodUID != "" && p.UID != h.PodUID) || (h.PodName != "" && p.Name != h.PodName) {
		return fmt.Errorf("%w: Pod %s/%s", execution.ErrIdentityConflict, p.Namespace, p.Name)
	}
	return nil
}
