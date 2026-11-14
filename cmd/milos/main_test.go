package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"context"
	v1 "github.com/wsq2333/milos/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestCatalogCLI(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = v1.AddToScheme(scheme)
	makeProfile := func(name, ns string) *v1.ResourceProfile {
		return &v1.ResourceProfile{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Spec: v1.ResourceProfileSpec{NodeSelector: map[string]string{"hardware.example/model": "h20"}}}
	}
	full := makeProfile("z-gpu", "team")
	full.Spec.Catalog = &v1.ProfileCatalogInfo{Description: "H20 for evaluation", WorkloadTypes: []string{"evaluation"}, CompatibilityRequirements: []string{"Use a platform-validated CUDA image"}}
	full.Spec.ResourceBounds = map[corev1.ResourceName]v1.ResourceBound{"nvidia.com/gpu": {Min: "1", Max: "8"}}
	r := fake.NewClientBuilder().WithScheme(scheme).WithObjects(full, makeProfile("a-legacy", "team"), makeProfile("hidden", "other")).Build()
	connect := func(o connection) (client.Reader, error) {
		if o.context != "test" || o.kubeconfig != "config" {
			t.Fatalf("connection flags lost: %+v", o)
		}
		return r, nil
	}
	call := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runCLI(args, &out, io.Discard, connect)
		return out.String(), err
	}
	out, err := call("profiles", "list", "--namespace", "team", "--context", "test", "--kubeconfig", "config")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "hidden") || strings.Index(out, "a-legacy") > strings.Index(out, "z-gpu") || !strings.Contains(out, "nvidia.com/gpu: 1..8") {
		t.Fatal(out)
	}
	out, err = call("profiles", "describe", "z-gpu", "--namespace", "team", "--context", "test", "--kubeconfig", "config")
	if err != nil || !strings.Contains(out, "not automatically verified") || !strings.Contains(out, "validated CUDA") || !strings.Contains(out, "evaluation") {
		t.Fatalf("%s %v", out, err)
	}
	out, err = call("profiles", "describe", "a-legacy", "--namespace", "team", "--context", "test", "--kubeconfig", "config")
	if err != nil || !strings.Contains(out, "compatibility is unknown") {
		t.Fatalf("%s %v", out, err)
	}
	out, err = call("profiles", "list", "--namespace", "empty", "--context", "test", "--kubeconfig", "config")
	if err != nil || !strings.Contains(out, "No ResourceProfiles") {
		t.Fatalf("%s %v", out, err)
	}
	_, err = call("profiles", "describe", "missing", "--namespace", "team", "--context", "test", "--kubeconfig", "config")
	if !apierrors.IsNotFound(err) {
		t.Fatalf("missing error: %v", err)
	}
	for _, args := range [][]string{{"profiles", "list", "--namespace", ""}, {"profiles", "list", "extra"}, {"profiles", "describe"}, {"profiles", "delete", "z-gpu"}} {
		if err := runCLI(args, io.Discard, io.Discard, func(connection) (client.Reader, error) {
			t.Fatal("invalid arguments accessed cluster")
			return nil, nil
		}); err == nil {
			t.Fatal("accepted", args)
		}
	}
}
func TestCompileDoesNotConnect(t *testing.T) {
	var out bytes.Buffer
	err := runCLI([]string{"--file", "../../examples/milos/profile-workflow.yaml", "--namespace", "default"}, &out, io.Discard, func(connection) (client.Reader, error) { t.Fatal("compile connected to cluster"); return nil, nil })
	if err != nil || !strings.Contains(out.String(), "kind: WorkflowRun") {
		t.Fatal(err, out.String())
	}
}
func TestCatalogForbidden(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = v1.AddToScheme(scheme)
	denied := apierrors.NewForbidden(schema.GroupResource{Group: v1.GroupVersion.Group, Resource: "resourceprofiles"}, "", errors.New("denied"))
	r := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error { return denied }}).Build()
	err := runCLI([]string{"profiles", "list"}, io.Discard, io.Discard, func(connection) (client.Reader, error) { return r, nil })
	if !apierrors.IsForbidden(err) {
		t.Fatalf("lost authorization error: %v", err)
	}
}
