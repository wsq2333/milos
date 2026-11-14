// Package catalog provides read-only, namespace-scoped ResourceProfile discovery.
package catalog

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"unicode"

	v1 "github.com/wsq2333/milos/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// List returns published profiles, not a statement about free capacity or compatibility.
func List(ctx context.Context, r client.Reader, namespace string) (*v1.ResourceProfileList, error) {
	list := &v1.ResourceProfileList{}
	if err := r.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list ResourceProfiles in %q: %w", namespace, err)
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })
	return list, nil
}
func Get(ctx context.Context, r client.Reader, namespace, name string) (*v1.ResourceProfile, error) {
	p := &v1.ResourceProfile{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, p); err != nil {
		return nil, fmt.Errorf("get ResourceProfile %s/%s: %w", namespace, name, err)
	}
	return p, nil
}

// plain removes terminal control characters from operator-authored text.
func plain(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}
func bounds(p v1.ResourceProfile) []string {
	keys := make([]string, 0, len(p.Spec.ResourceBounds))
	for k := range p.Spec.ResourceBounds {
		keys = append(keys, string(k))
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		b := p.Spec.ResourceBounds[corev1.ResourceName(k)]
		out = append(out, plain(k+": "+b.Min+".."+b.Max))
	}
	return out
}
func WriteList(w io.Writer, namespace string, list *v1.ResourceProfileList) error {
	if len(list.Items) == 0 {
		_, err := fmt.Fprintf(w, "No ResourceProfiles published in namespace %s.\n", plain(namespace))
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "NAME\tDESCRIPTION\tRESOURCE BOUNDS"); err != nil {
		return err
	}
	for _, p := range list.Items {
		description := "Not provided"
		if p.Spec.Catalog != nil && p.Spec.Catalog.Description != "" {
			description = plain(p.Spec.Catalog.Description)
		}
		b := strings.Join(bounds(p), ", ")
		if b == "" {
			b = "Not specified"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\n", plain(p.Name), description, b); err != nil {
			return err
		}
	}
	return tw.Flush()
}
func WriteDetail(w io.Writer, p *v1.ResourceProfile) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Name: %s\nNamespace: %s\nExecution support: native-pod / default-scheduler\n", plain(p.Name), plain(p.Namespace))
	c := p.Spec.Catalog
	description := "Not provided"
	if c != nil && c.Description != "" {
		description = plain(c.Description)
	}
	fmt.Fprintf(&b, "Description: %s\nWorkload types (guidance):\n", description)
	if c == nil || len(c.WorkloadTypes) == 0 {
		b.WriteString("  Not provided\n")
	} else {
		for _, v := range c.WorkloadTypes {
			fmt.Fprintf(&b, "  - %s\n", plain(v))
		}
	}
	b.WriteString("Resource bounds (enforced effective requests; limits must not exceed maximum):\n")
	bs := bounds(*p)
	if len(bs) == 0 {
		b.WriteString("  Not specified; other platform policies still apply\n")
	} else {
		for _, v := range bs {
			fmt.Fprintf(&b, "  - %s\n", v)
		}
	}
	b.WriteString("Compatibility requirements (operator guidance; not automatically verified):\n")
	if c == nil || len(c.CompatibilityRequirements) == 0 {
		b.WriteString("  Not provided; compatibility is unknown\n")
	} else {
		for _, v := range c.CompatibilityRequirements {
			fmt.Fprintf(&b, "  - %s\n", plain(v))
		}
	}
	b.WriteString("Node selectors (enforced):\n")
	keys := make([]string, 0, len(p.Spec.NodeSelector))
	for k := range p.Spec.NodeSelector {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "  %s=%s\n", plain(k), plain(p.Spec.NodeSelector[k]))
	}
	b.WriteString("Tolerations (enforced):\n")
	if len(p.Spec.Tolerations) == 0 {
		b.WriteString("  None\n")
	}
	for _, t := range p.Spec.Tolerations {
		fmt.Fprintf(&b, "  key=%s operator=%s value=%s effect=%s\n", plain(t.Key), plain(string(t.Operator)), plain(t.Value), plain(string(t.Effect)))
	}
	b.WriteString("Published profiles do not guarantee free capacity or reserve resources.\n")
	_, err := io.WriteString(w, b.String())
	return err
}
