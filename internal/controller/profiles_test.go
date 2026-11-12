package controller

import (
	"testing"

	v1 "github.com/wsq2333/milos/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestMissingProfileBlocksWholeWorkflow(t *testing.T) {
	h := newHarness(t)
	r := h.run()
	r.Spec.Definition.Nodes[1].Requirements = &v1.TaskRequirements{ProfileRef: v1.ProfileReference{Name: "missing"}}
	if err := h.c.Update(h.ctx, r); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		h.step()
	}
	pods := &corev1.PodList{}
	if err := h.c.List(h.ctx, pods); err != nil {
		t.Fatal(err)
	}
	if len(pods.Items) != 0 || h.run().Status.DefinitionDigest != "" {
		t.Fatal("partially executed unresolved workflow")
	}
	p := &v1.ResourceProfile{ObjectMeta: metav1.ObjectMeta{Name: "missing", Namespace: "default"}, Spec: v1.ResourceProfileSpec{NodeSelector: map[string]string{"hardware.example/model": "h20"}}}
	if err := h.c.Create(h.ctx, p); err != nil {
		t.Fatal(err)
	}
	h.step()
	if len(h.run().Status.ResolvedTasks) != 1 {
		t.Fatal("did not recover after profile publication")
	}
}

func TestCorruptSnapshotBlocksCreation(t *testing.T) {
	h := newHarness(t)
	r := h.run()
	r.Spec.Definition.Nodes[0].Requirements = &v1.TaskRequirements{ProfileRef: v1.ProfileReference{Name: "gpu"}}
	if err := h.c.Update(h.ctx, r); err != nil {
		t.Fatal(err)
	}
	p := &v1.ResourceProfile{ObjectMeta: metav1.ObjectMeta{Name: "gpu", Namespace: "default"}, Spec: v1.ResourceProfileSpec{NodeSelector: map[string]string{"hardware.example/model": "h20"}}}
	if err := h.c.Create(h.ctx, p); err != nil {
		t.Fatal(err)
	}
	h.step()
	h.step()
	r = h.run()
	snap := r.Status.ResolvedTasks["prepare"]
	snap.Constraints.NodeSelector["hardware.example/model"] = "other"
	r.Status.ResolvedTasks["prepare"] = snap
	if err := h.c.Status().Update(h.ctx, r); err != nil {
		t.Fatal(err)
	}
	h.step()
	pods := &corev1.PodList{}
	if err := h.c.List(h.ctx, pods, client.InNamespace("default")); err != nil {
		t.Fatal(err)
	}
	if len(pods.Items) != 0 {
		t.Fatal("corrupt snapshot dispatched")
	}
}
