package execution

import (
	"fmt"

	"k8s.io/apimachinery/pkg/util/validation"
)

// ValidateConfiguration is the explicit support matrix, independent of installed controllers.
func ValidateConfiguration(c Configuration) error {
	if c.Scheduler != "default-scheduler" && c.Scheduler != "volcano" {
		return fmt.Errorf("unsupported scheduler %q", c.Scheduler)
	}
	if c.Scheduler == "default-scheduler" && c.VolcanoQueue != "" {
		return fmt.Errorf("queueName requires the volcano scheduler")
	}
	if c.VolcanoQueue != "" && len(validation.IsDNS1123Subdomain(c.VolcanoQueue)) != 0 {
		return fmt.Errorf("invalid Volcano queueName")
	}
	switch c.Backend {
	case NativeBackend:
		if c.QueueName != "" || c.AdmissionCheck != "" || len(c.WorkerClusters) != 0 {
			return fmt.Errorf("native-pod cannot configure Kueue or MultiKueue")
		}
	case KueueBackend, MultiKueueBackend:
		if c.QueueName == "" || len(validation.IsDNS1123Subdomain(c.QueueName)) != 0 {
			return fmt.Errorf("a valid LocalQueue queueRef is required")
		}
		if c.Backend == KueueBackend {
			if c.AdmissionCheck != "" || len(c.WorkerClusters) != 0 {
				return fmt.Errorf("local Kueue cannot configure remote placement")
			}
		} else {
			if c.Scheduler != "default-scheduler" {
				return fmt.Errorf("MultiKueue with Volcano is not supported; remote PodGroup lifecycle requires a separate integration")
			}
			if c.AdmissionCheck == "" || len(validation.IsDNS1123Subdomain(c.AdmissionCheck)) != 0 || len(c.WorkerClusters) == 0 || len(c.WorkerClusters) > 16 {
				return fmt.Errorf("MultiKueue requires an admissionCheck and 1..16 workerClusters")
			}
			seen := map[string]bool{}
			for _, name := range c.WorkerClusters {
				if len(validation.IsDNS1123Subdomain(name)) != 0 || seen[name] {
					return fmt.Errorf("invalid or duplicate worker cluster %q", name)
				}
				seen[name] = true
			}
		}
	default:
		return fmt.Errorf("unsupported backend %q", c.Backend)
	}
	return nil
}
