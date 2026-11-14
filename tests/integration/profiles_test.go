//go:build integration

package integration

import (
	"bytes"
	"context"
	"github.com/wsq2333/milos/internal/catalog"
	"os"
	"strings"
	"testing"

	v1 "github.com/wsq2333/milos/api/v1alpha1"
	"github.com/wsq2333/milos/internal/authoring"
	"github.com/wsq2333/milos/internal/backends/nativepod"
	"github.com/wsq2333/milos/internal/controller"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func testProfileAPI(t *testing.T, c client.Client, ns string) {
	ctx := context.Background()
	p := &v1.ResourceProfile{ObjectMeta: metav1.ObjectMeta{Name: "h20-exclusive-v1", Namespace: ns}, Spec: v1.ResourceProfileSpec{NodeSelector: map[string]string{"accelerator.platform.example/model": "h20"}, ResourceBounds: map[corev1.ResourceName]v1.ResourceBound{"nvidia.com/gpu": {Min: "1", Max: "8"}}}}
	p.Spec.Catalog = &v1.ProfileCatalogInfo{Description: "Verified H20 configuration", CompatibilityRequirements: []string{"Use a validated CUDA image"}}
	if err := c.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	items, err := catalog.List(ctx, c, ns)
	if err != nil || len(items.Items) != 1 {
		t.Fatalf("catalog list: %v %+v", err, items)
	}
	detail, err := catalog.Get(ctx, c, ns, p.Name)
	if err != nil || detail.Spec.Catalog == nil || detail.Spec.Catalog.Description != p.Spec.Catalog.Description {
		t.Fatalf("catalog round trip: %v %+v", err, detail)
	}
	var text bytes.Buffer
	if err := catalog.WriteDetail(&text, detail); err != nil || !strings.Contains(text.String(), "validated CUDA") {
		t.Fatalf("catalog detail: %v %s", err, text.String())
	}
	invalid := p.DeepCopy()
	invalid.Name = "oversized-catalog"
	invalid.ResourceVersion = ""
	invalid.UID = ""
	invalid.Spec.Catalog.Description = strings.Repeat("x", 2049)
	if err := c.Create(ctx, invalid); !apierrors.IsInvalid(err) {
		t.Fatalf("oversized catalog accepted: %v", err)
	}
	catalogChange := p.DeepCopy()
	catalogChange.Spec.Catalog.Description = "changed"
	if err := c.Update(ctx, catalogChange); !apierrors.IsInvalid(err) {
		t.Fatalf("catalog mutation accepted: %v", err)
	}
	changed := p.DeepCopy()
	changed.Spec.NodeSelector["accelerator.platform.example/model"] = "other"
	if err := c.Update(ctx, changed); !apierrors.IsInvalid(err) {
		t.Fatalf("profile mutation accepted: %v", err)
	}
	data, err := os.ReadFile("../../examples/milos/profile-workflow.yaml")
	if err != nil {
		t.Fatal(err)
	}
	run, err := authoring.Compile(data, ns)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	key := client.ObjectKeyFromObject(run)
	step := func() {
		t.Helper()
		r := &controller.WorkflowRunReconciler{Client: c, Reader: c, Backend: &nativepod.Backend{Client: c, Reader: c}}
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatal(err)
		}
		if err := c.Get(ctx, key, run); err != nil {
			t.Fatal(err)
		}
	}
	step()
	step()
	if len(run.Status.ResolvedTasks) != 1 {
		t.Fatal("snapshot not persisted")
	}
	// Remove the source before intent creation: a fresh controller must use the snapshot.
	if err = c.Delete(ctx, p); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12 && run.Status.Nodes["evaluate"].PodUID == ""; i++ {
		step()
	}
	s := run.Status.Nodes["evaluate"]
	if s.PodUID == "" || s.Decision == nil {
		t.Fatal("execution was not persisted")
	}
	pod := &corev1.Pod{}
	if err = c.Get(ctx, client.ObjectKey{Namespace: ns, Name: s.PodName}, pod); err != nil {
		t.Fatal(err)
	}
	if pod.Spec.NodeSelector["accelerator.platform.example/model"] != "h20" {
		t.Fatal("lost selector")
	}
	q := pod.Spec.Containers[0].Resources.Requests["nvidia.com/gpu"]
	if q.String() != "1" {
		t.Fatal("GPU request not normalized")
	}
	uid := pod.UID
	step()
	step()
	if err = c.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil {
		t.Fatal(err)
	}
	if pod.UID != uid {
		t.Fatal("duplicated execution")
	}
	run.Spec.DesiredState = "Cancelled"
	if err = c.Update(ctx, run); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12 && run.Status.Phase != "Cancelled"; i++ {
		step()
	}
	if run.Status.Phase != "Cancelled" {
		t.Fatal("profile deletion blocked cleanup")
	}
}
