package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ResourceProfile is a platform-managed, immutable set of enforced Pod constraints.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
type ResourceProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="profile spec is immutable; publish a new version"
	Spec ResourceProfileSpec `json:"spec"`
}

// +kubebuilder:object:root=true
type ResourceProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ResourceProfile `json:"items"`
}

// ResourceProfileSpec contains enforceable constraints, not inventory or quota claims.
type ResourceProfileSpec struct {
	// +kubebuilder:validation:MinProperties=1
	// +kubebuilder:validation:MaxProperties=32
	NodeSelector map[string]string `json:"nodeSelector"`
	// +optional
	// +kubebuilder:validation:MaxItems=16
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
	// ResourceBounds constrains effective requests by resource name.
	// +optional
	ResourceBounds map[corev1.ResourceName]ResourceBound `json:"resourceBounds,omitempty"`
}

type ResourceBound struct {
	Min string `json:"min"`
	Max string `json:"max"`
}

type TaskRequirements struct {
	ProfileRef ProfileReference `json:"profileRef"`
}

type ProfileReference struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// ResolvedTask is committed before any node in the workflow is dispatched.
type ResolvedTask struct {
	ProfileName       string                      `json:"profileName"`
	ProfileUID        string                      `json:"profileUID"`
	ProfileGeneration int64                       `json:"profileGeneration"`
	CompilerVersion   string                      `json:"compilerVersion"`
	Digest            string                      `json:"digest"`
	Constraints       ResourceProfileSpec         `json:"constraints"`
	Resources         corev1.ResourceRequirements `json:"resources"`
}

type ExecutionDecision struct {
	ResolvedTaskDigest string `json:"resolvedTaskDigest"`
	ExecutionDigest    string `json:"executionDigest"`
	Reason             string `json:"reason"`
}
