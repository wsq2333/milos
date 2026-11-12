// Package authoring compiles a strict, finite Argo-style Milos document.
package authoring

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	v1 "github.com/wsq2333/milos/api/v1alpha1"
	"github.com/wsq2333/milos/internal/controller"
	yamlv3 "go.yaml.in/yaml/v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/yaml"
)

type document struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace,omitempty"`
	} `json:"metadata"`
	Spec struct {
		Entrypoint         string                        `json:"entrypoint"`
		Parallelism        *int32                        `json:"parallelism,omitempty"`
		Templates          []template                    `json:"templates"`
		Volumes            []corev1.Volume               `json:"volumes,omitempty"`
		ServiceAccountName string                        `json:"serviceAccountName,omitempty"`
		ImagePullSecrets   []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
	} `json:"spec"`
}
type template struct {
	Name string `json:"name"`
	DAG  *struct {
		FailFast *bool  `json:"failFast"`
		Tasks    []task `json:"tasks"`
	} `json:"dag,omitempty"`
	Container *v1.ContainerSpec `json:"container,omitempty"`
}
type task struct {
	Name         string   `json:"name"`
	Template     string   `json:"template"`
	Dependencies []string `json:"dependencies,omitempty"`
	Milos        *struct {
		Requirements *v1.TaskRequirements `json:"requirements"`
	} `json:"milos,omitempty"`
}

// Compile performs no API writes. Namespace is an explicit submission boundary.
func Compile(data []byte, namespace string) (*v1.WorkflowRun, error) {
	if len(data) == 0 || len(data) > 256*1024 {
		return nil, fmt.Errorf("input must be 1..262144 bytes")
	}
	if namespace == "" || len(validation.IsDNS1123Label(namespace)) > 0 {
		return nil, fmt.Errorf("invalid namespace")
	}
	dec := yamlv3.NewDecoder(bytes.NewReader(data))
	var root yamlv3.Node
	if err := dec.Decode(&root); err != nil {
		return nil, err
	}
	var extra yamlv3.Node
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("exactly one YAML document required")
	}
	var walk func(*yamlv3.Node, int) error
	walk = func(n *yamlv3.Node, depth int) error {
		if depth > 32 || n.Anchor != "" || n.Kind == yamlv3.AliasNode || n.Value == "<<" {
			return fmt.Errorf("anchors, aliases, merge keys or excessive nesting are unsupported")
		}
		switch n.Tag {
		case "", "!!map", "!!seq", "!!str", "!!int", "!!bool", "!!null", "!!float":
		default:
			return fmt.Errorf("unsupported YAML tag %q", n.Tag)
		}
		if n.Kind == yamlv3.ScalarNode && strings.Contains(n.Value, "{{") {
			return fmt.Errorf("parameter interpolation is not supported by this initial compiler")
		}
		for _, c := range n.Content {
			if err := walk(c, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(&root, 0); err != nil {
		return nil, err
	}
	j, err := yaml.YAMLToJSONStrict(data)
	if err != nil {
		return nil, err
	}
	var d document
	jd := json.NewDecoder(bytes.NewReader(j))
	jd.DisallowUnknownFields()
	if err = jd.Decode(&d); err != nil {
		return nil, err
	}
	if d.APIVersion != v1.GroupVersion.String() || d.Kind != "Workflow" {
		return nil, fmt.Errorf("expected milos.example.io/v1alpha1 Workflow; standard Argo import is not implemented")
	}
	if d.Metadata.Name == "" || len(validation.IsDNS1123Subdomain(d.Metadata.Name)) > 0 {
		return nil, fmt.Errorf("metadata.name must be a valid explicit name")
	}
	if d.Metadata.Namespace != "" && d.Metadata.Namespace != namespace {
		return nil, fmt.Errorf("metadata.namespace differs from submission namespace")
	}
	templates := map[string]template{}
	if len(d.Spec.Templates) > 129 {
		return nil, fmt.Errorf("too many templates")
	}
	for _, t := range d.Spec.Templates {
		if t.Name == "" || len(validation.IsDNS1123Label(t.Name)) > 0 {
			return nil, fmt.Errorf("invalid template name")
		}
		if _, ok := templates[t.Name]; ok {
			return nil, fmt.Errorf("duplicate template %q", t.Name)
		}
		if (t.DAG == nil) == (t.Container == nil) {
			return nil, fmt.Errorf("template %q must have exactly one of dag/container", t.Name)
		}
		if t.DAG != nil && t.Name != d.Spec.Entrypoint {
			return nil, fmt.Errorf("only the entrypoint may contain a DAG")
		}
		templates[t.Name] = t
	}
	entry, ok := templates[d.Spec.Entrypoint]
	if !ok || entry.DAG == nil {
		return nil, fmt.Errorf("entrypoint must reference a DAG")
	}
	if entry.DAG.FailFast == nil || *entry.DAG.FailFast {
		return nil, fmt.Errorf("dag.failFast must explicitly be false")
	}
	run := &v1.WorkflowRun{TypeMeta: metav1.TypeMeta{APIVersion: v1.GroupVersion.String(), Kind: "WorkflowRun"}, ObjectMeta: metav1.ObjectMeta{Name: d.Metadata.Name, Namespace: namespace}, Spec: v1.WorkflowRunSpec{DesiredState: "Running", Execution: v1.ExecutionConfig{Backend: "native-pod", ContractVersion: "v1", Admission: v1.AdmissionConfig{Provider: "None"}, Placement: v1.PlacementConfig{Provider: "Local"}, Scheduler: v1.SchedulerConfig{Name: "default-scheduler"}}, ExecutionPolicy: v1.ExecutionPolicy{Parallelism: 1}, Definition: v1.WorkflowDefinition{Volumes: d.Spec.Volumes, ServiceAccountName: d.Spec.ServiceAccountName, ImagePullSecrets: d.Spec.ImagePullSecrets}}}
	if d.Spec.Parallelism != nil {
		run.Spec.ExecutionPolicy.Parallelism = *d.Spec.Parallelism
	}
	for _, t := range entry.DAG.Tasks {
		tmpl, ok := templates[t.Template]
		if !ok || tmpl.Container == nil {
			return nil, fmt.Errorf("task %q must reference a container template", t.Name)
		}
		n := v1.NodeSpec{Name: t.Name, Dependencies: t.Dependencies, Container: *tmpl.Container.DeepCopy()}
		if t.Milos != nil {
			if t.Milos.Requirements == nil {
				return nil, fmt.Errorf("task %q: milos.requirements is required when milos is present", t.Name)
			}
			n.Requirements = t.Milos.Requirements
		}
		run.Spec.Definition.Nodes = append(run.Spec.Definition.Nodes, n)
	}
	for _, n := range run.Spec.Definition.Nodes {
		for _, e := range n.Container.Env {
			if e.ValueFrom != nil {
				if e.Value != "" {
					return nil, fmt.Errorf("env value and valueFrom conflict")
				}
				f := e.ValueFrom
				if f.FieldRef != nil || f.ResourceFieldRef != nil || (f.SecretKeyRef == nil) == (f.ConfigMapKeyRef == nil) {
					return nil, fmt.Errorf("only one secretKeyRef or configMapKeyRef is supported")
				}
			}
		}
		for _, m := range n.Container.VolumeMounts {
			if m.SubPath != "" || m.SubPathExpr != "" || m.MountPropagation != nil || m.RecursiveReadOnly != nil {
				return nil, fmt.Errorf("unsupported volumeMount field")
			}
		}
	}
	if err := controller.ValidateDefinition(run); err != nil {
		return nil, err
	}
	return run, nil
}
