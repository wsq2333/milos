// Package resources resolves platform profiles into replayable Pod constraints.
package resources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	v1 "github.com/wsq2333/milos/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func Digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func ValidateProfile(s v1.ResourceProfileSpec) error {
	if len(s.NodeSelector) == 0 || len(s.NodeSelector) > 32 || len(s.Tolerations) > 16 || len(s.ResourceBounds) > 32 {
		return fmt.Errorf("profile constraint count out of bounds")
	}
	for k, v := range s.NodeSelector {
		if len(validation.IsQualifiedName(k)) > 0 || len(validation.IsValidLabelValue(v)) > 0 {
			return fmt.Errorf("invalid node selector %q", k)
		}
	}
	for _, t := range s.Tolerations {
		if t.TolerationSeconds != nil {
			return fmt.Errorf("timed tolerations are unsupported")
		}
		if t.Key == "" || len(validation.IsQualifiedName(t.Key)) > 0 {
			return fmt.Errorf("toleration requires a valid explicit key")
		}
		if t.Operator != corev1.TolerationOpExists && t.Operator != corev1.TolerationOpEqual {
			return fmt.Errorf("toleration requires Equal or Exists")
		}
		if t.Operator == corev1.TolerationOpExists && t.Value != "" {
			return fmt.Errorf("Exists toleration cannot have a value")
		}
		if t.Effect != corev1.TaintEffectNoSchedule && t.Effect != corev1.TaintEffectPreferNoSchedule {
			return fmt.Errorf("only scheduling tolerations are supported")
		}
	}
	for k, b := range s.ResourceBounds {
		lo, e := resource.ParseQuantity(b.Min)
		hi, f := resource.ParseQuantity(b.Max)
		if len(validation.IsQualifiedName(string(k))) > 0 || e != nil || f != nil || lo.Sign() < 0 || hi.Cmp(lo) < 0 {
			return fmt.Errorf("invalid resource bound %q", k)
		}
	}
	return nil
}

func Normalize(r corev1.ResourceRequirements) corev1.ResourceRequirements {
	out := *r.DeepCopy()
	if out.Requests == nil {
		out.Requests = corev1.ResourceList{}
	}
	for k, v := range out.Limits {
		if _, ok := out.Requests[k]; !ok {
			out.Requests[k] = v.DeepCopy()
		}
	}
	return out
}

func Resolve(ctx context.Context, reader client.Reader, run *v1.WorkflowRun) (map[string]v1.ResolvedTask, error) {
	var result map[string]v1.ResolvedTask
	for _, n := range run.Spec.Definition.Nodes {
		if n.Requirements == nil {
			continue
		}
		if run.Spec.Execution.Backend != "native-pod" || run.Spec.Execution.Scheduler.Name != "default-scheduler" {
			return nil, fmt.Errorf("node %q: profiles currently require native-pod/default-scheduler", n.Name)
		}
		name := n.Requirements.ProfileRef.Name
		if len(validation.IsDNS1123Subdomain(name)) > 0 || name == "" {
			return nil, fmt.Errorf("node %q: invalid profileRef.name", n.Name)
		}
		p := &v1.ResourceProfile{}
		if err := reader.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: name}, p); err != nil {
			return nil, fmt.Errorf("node %q profile %q: %w", n.Name, name, err)
		}
		if err := ValidateProfile(p.Spec); err != nil {
			return nil, fmt.Errorf("profile %q: %w", name, err)
		}
		rr := Normalize(n.Container.Resources)
		for k, b := range p.Spec.ResourceBounds {
			lo := resource.MustParse(b.Min)
			hi := resource.MustParse(b.Max)
			q := rr.Requests[k]
			if q.Cmp(lo) < 0 || q.Cmp(hi) > 0 {
				return nil, fmt.Errorf("node %q: request %s outside profile bounds", n.Name, k)
			}
			if lim, ok := rr.Limits[k]; ok && lim.Cmp(hi) > 0 {
				return nil, fmt.Errorf("node %q: limit %s exceeds profile bound", n.Name, k)
			}
		}
		snap := v1.ResolvedTask{ProfileName: name, ProfileUID: string(p.UID), ProfileGeneration: p.Generation, CompilerVersion: "native-profile-v1", Constraints: *p.Spec.DeepCopy(), Resources: rr}
		snap.Digest = Digest(snap)
		if result == nil {
			result = map[string]v1.ResolvedTask{}
		}
		result[n.Name] = snap
	}
	b, _ := json.Marshal(result)
	if len(b) > 256*1024 {
		return nil, fmt.Errorf("resolved profiles exceed 256 KiB")
	}
	return result, nil
}

func Verify(s v1.ResolvedTask) bool { d := s.Digest; s.Digest = ""; return d != "" && d == Digest(s) }
