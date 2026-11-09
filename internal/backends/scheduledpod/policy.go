package scheduledpod

import (
	"context"
	"fmt"
	"slices"

	"github.com/wsq2333/milos/internal/execution"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// validatePolicy checks the queue's actual routing rather than trusting the requested mode.
func (b *Backend) validatePolicy(ctx context.Context, r execution.Request) error {
	c := r.Configuration
	if err := execution.ValidateConfiguration(c); err != nil {
		return err
	}
	if c.Scheduler == "volcano" {
		if !b.EnableVolcano {
			return fmt.Errorf("Volcano integration is disabled")
		}
		queue := c.VolcanoQueue
		if queue == "" {
			queue = "default"
		}
		if _, err := get(ctx, b.Reader, VolcanoQueueGVK, "", queue); err != nil {
			return fmt.Errorf("Volcano Queue: %w", err)
		}
	}
	if c.Backend == execution.NativeBackend {
		return nil
	}
	if !b.EnableKueue {
		return fmt.Errorf("Kueue integration is disabled")
	}
	if c.Backend == execution.MultiKueueBackend && !b.EnableMultiKueue {
		return fmt.Errorf("MultiKueue integration is disabled")
	}
	lq, err := get(ctx, b.Reader, LocalQueueGVK, r.Namespace, c.QueueName)
	if err != nil {
		return fmt.Errorf("LocalQueue: %w", err)
	}
	cqName := field(lq, "spec", "clusterQueue")
	if cqName == "" {
		return fmt.Errorf("LocalQueue has no clusterQueue")
	}
	cq, err := get(ctx, b.Reader, ClusterQueueGVK, "", cqName)
	if err != nil {
		return fmt.Errorf("ClusterQueue: %w", err)
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(cq.Object, "spec", "concurrentAdmissionPolicy"); found {
		return fmt.Errorf("concurrent admission variants are not supported")
	}
	checks, _, err := unstructured.NestedSlice(cq.Object, "spec", "admissionChecksStrategy", "admissionChecks")
	if err != nil {
		return err
	}
	found := false
	for _, raw := range checks {
		rule, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("invalid ClusterQueue admission check")
		}
		name, ok := rule["name"].(string)
		if !ok {
			return fmt.Errorf("missing admission check name")
		}
		ac, err := get(ctx, b.Reader, AdmissionCheckGVK, "", name)
		if err != nil {
			return err
		}
		if field(ac, "spec", "controllerName") != MultiController {
			continue
		}
		if c.Backend != execution.MultiKueueBackend {
			return fmt.Errorf("local execution queue routes to MultiKueue")
		}
		if name != c.AdmissionCheck || found {
			return fmt.Errorf("ClusterQueue MultiKueue check differs from the execution snapshot")
		}
		flavors, _, err := unstructured.NestedSlice(rule, "onFlavors")
		if err != nil || len(flavors) > 0 {
			return fmt.Errorf("MultiKueue admission check must apply to every flavor")
		}
		if field(ac, "spec", "parameters", "apiGroup") != WorkloadGVK.Group || field(ac, "spec", "parameters", "kind") != "MultiKueueConfig" {
			return fmt.Errorf("invalid MultiKueue admission check parameters")
		}
		config, err := get(ctx, b.Reader, MultiConfigGVK, "", field(ac, "spec", "parameters", "name"))
		if err != nil {
			return err
		}
		clusters, _, err := unstructured.NestedStringSlice(config.Object, "spec", "clusters")
		if err != nil {
			return err
		}
		expected := slices.Clone(c.WorkerClusters)
		slices.Sort(expected)
		slices.Sort(clusters)
		if !slices.Equal(expected, clusters) {
			return fmt.Errorf("MultiKueue worker set differs from immutable execution snapshot")
		}
		for _, worker := range clusters {
			if b.Workers[worker] == nil {
				return fmt.Errorf("no read-only client configured for worker %q", worker)
			}
		}
		if b.Origin == "" {
			return fmt.Errorf("MultiKueue origin must be configured")
		}
		found = true
	}
	if c.Backend == execution.MultiKueueBackend && !found {
		return fmt.Errorf("queue does not reference the requested MultiKueue admission check")
	}
	return nil
}
