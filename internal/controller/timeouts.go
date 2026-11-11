package controller

import (
	"fmt"
	"time"

	milosv1 "github.com/wsq2333/milos/api/v1alpha1"
	"github.com/wsq2333/milos/internal/workflow"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func validateTimeouts(t *milosv1.NodeTimeouts) error {
	if t != nil {
		for _, v := range []*int64{t.TotalSeconds, t.RunSeconds} {
			if v != nil && (*v < 0 || *v > 604800) {
				return fmt.Errorf("node timeout must be 0..604800 seconds")
			}
		}
	}
	return nil
}

func nodeTimeouts(defaults, overrides *milosv1.NodeTimeouts) (total, run int64) {
	for _, t := range []*milosv1.NodeTimeouts{defaults, overrides} {
		if t == nil {
			continue
		}
		if t.TotalSeconds != nil {
			total = *t.TotalSeconds
		}
		if t.RunSeconds != nil {
			run = *t.RunSeconds
		}
	}
	return
}

func (r *WorkflowRunReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func timestamp(t time.Time) *metav1.Time {
	v := metav1.NewTime(t)
	return &v
}

func setStage(s *milosv1.NodeStatus, stage string, now time.Time) {
	if s.Stage != stage || s.StageSince == nil {
		s.Stage, s.StageSince = stage, timestamp(now)
	}
}

func diagnostic(message string) string {
	chars := []rune(message)
	if len(chars) > 1024 {
		return string(chars[:1024])
	}
	return message
}

// nodeDeadline returns the earliest enabled budget. A total/run tie favors total.
func nodeDeadline(s milosv1.NodeStatus, total, run int64) (time.Time, string) {
	var deadline time.Time
	var reason string
	if total > 0 && s.IntentRecordedAt != nil {
		deadline = s.IntentRecordedAt.Add(time.Duration(total) * time.Second)
		reason = "NodeDeadlineExceeded"
	}
	if run > 0 && s.StartedAt != nil {
		candidate := s.StartedAt.Add(time.Duration(run) * time.Second)
		if deadline.IsZero() || candidate.Before(deadline) {
			deadline, reason = candidate, "RunDeadlineExceeded"
		}
	}
	return deadline, reason
}

// A verified terminal timestamp at or before the deadline wins over a late poll.
// Without terminal timing evidence, an expired budget starts the stop protocol.
func nodeTimeoutReason(s milosv1.NodeStatus, total, run int64, now time.Time) string {
	deadline, reason := nodeDeadline(s, total, run)
	if deadline.IsZero() {
		return ""
	}
	if workflow.Terminal(workflow.Phase(s.Phase)) && s.FinishedAt != nil {
		if s.FinishedAt.After(deadline) {
			return reason
		}
		return ""
	}
	if !now.Before(deadline) {
		return reason
	}
	return ""
}
