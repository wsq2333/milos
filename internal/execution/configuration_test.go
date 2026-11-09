package execution

import "testing"

func TestSupportMatrix(t *testing.T) {
	for _, backend := range []string{NativeBackend, KueueBackend, MultiKueueBackend} {
		for _, scheduler := range []string{"default-scheduler", "volcano"} {
			t.Run(backend+"/"+scheduler, func(t *testing.T) {
				c := Configuration{Backend: backend, Scheduler: scheduler}
				if backend != NativeBackend {
					c.QueueName = "local"
				}
				if backend == MultiKueueBackend {
					c.AdmissionCheck = "dispatch"
					c.WorkerClusters = []string{"worker"}
				}
				err := ValidateConfiguration(c)
				supported := backend != MultiKueueBackend || scheduler != "volcano"
				if (err == nil) != supported {
					t.Fatalf("support matrix mismatch: %v", err)
				}
			})
		}
	}
}
