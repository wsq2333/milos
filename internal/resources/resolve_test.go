package resources

import (
	"context"
	"testing"

	v1 "github.com/wsq2333/milos/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestResolve(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = v1.AddToScheme(scheme)
	p := &v1.ResourceProfile{ObjectMeta: metav1.ObjectMeta{Name: "gpu", Namespace: "test", UID: "profile-uid"}, Spec: v1.ResourceProfileSpec{NodeSelector: map[string]string{"hardware.example/model": "h20"}, ResourceBounds: map[corev1.ResourceName]v1.ResourceBound{"nvidia.com/gpu": {Min: "1", Max: "8"}}}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(p).Build()
	run := &v1.WorkflowRun{ObjectMeta: metav1.ObjectMeta{Namespace: "test"}, Spec: v1.WorkflowRunSpec{Execution: v1.ExecutionConfig{Backend: "native-pod", Scheduler: v1.SchedulerConfig{Name: "default-scheduler"}}, Definition: v1.WorkflowDefinition{Nodes: []v1.NodeSpec{{Name: "one", Requirements: &v1.TaskRequirements{ProfileRef: v1.ProfileReference{Name: "gpu"}}, Container: v1.ContainerSpec{Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")}}}}}}}}
	result, err := Resolve(context.Background(), c, run)
	if err != nil {
		t.Fatal(err)
	}
	if !Verify(result["one"]) {
		t.Fatal("invalid snapshot digest")
	}
	original := result["one"]
	corrupt := original.DeepCopy()
	corrupt.Constraints.NodeSelector["hardware.example/model"] = "other"
	if Verify(*corrupt) {
		t.Fatal("accepted corrupt snapshot")
	}
	run.Spec.Definition.Nodes[0].Container.Resources.Limits["nvidia.com/gpu"] = resource.MustParse("9")
	if _, err = Resolve(context.Background(), c, run); err == nil {
		t.Fatal("accepted out of bounds")
	}
	run.Namespace = "other"
	if _, err = Resolve(context.Background(), c, run); err == nil {
		t.Fatal("cross namespace lookup")
	}
	run.Spec.Definition.Nodes[0].Requirements = nil
	if got, err := Resolve(context.Background(), c, run); err != nil || got != nil {
		t.Fatal("legacy resolution changed")
	}
}
