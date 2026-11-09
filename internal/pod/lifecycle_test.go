package pod

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"
	"time"
)

func TestObservationDistinguishesWaitingStages(t *testing.T) {
	for _, test := range []struct {
		name, node, stage, reason string
		gates                     bool
		condition                 *corev1.PodCondition
		waiting                   *corev1.ContainerStateWaiting
	}{
		{name: "unscheduled", stage: "WaitingForScheduling"},
		{name: "unschedulable", stage: "WaitingForScheduling", reason: "Unschedulable", condition: &corev1.PodCondition{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable", Message: "Insufficient nvidia.com/gpu"}},
		{name: "gated", stage: "WaitingForAdmission", reason: "SchedulingGated", gates: true},
		{name: "image", node: "worker", stage: "WaitingForStartup", reason: "ImagePullBackOff", waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "image unavailable"}},
		{name: "starting", node: "worker", stage: "WaitingForStartup"},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := &corev1.Pod{Spec: corev1.PodSpec{NodeName: test.node}, Status: corev1.PodStatus{Phase: corev1.PodPending}}
			if test.gates {
				p.Spec.SchedulingGates = []corev1.PodSchedulingGate{{Name: "test"}}
			}
			if test.condition != nil {
				p.Status.Conditions = []corev1.PodCondition{*test.condition}
			}
			if test.waiting != nil {
				p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", State: corev1.ContainerState{Waiting: test.waiting}}}
			}
			o := ObservePod(p)
			if o.Phase != "Submitted" || o.Stage != test.stage || o.Reason != test.reason || o.StartedAt != nil {
				t.Fatalf("wrong observation: %+v", o)
			}
			if (test.condition != nil || test.waiting != nil) && o.Message == "" {
				t.Fatal("missing diagnostic message")
			}
		})
	}
}

func TestMainContainerTimingSurvivesSkippedRunningObservation(t *testing.T) {
	start := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	finish := start.Add(time.Minute)
	p := &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodSucceeded, ContainerStatuses: []corev1.ContainerStatus{
		{Name: "other", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(start.Add(-time.Hour))}}},
		{Name: "main", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{StartedAt: metav1.NewTime(start), FinishedAt: metav1.NewTime(finish)}}},
	}}}
	o := ObservePod(p)
	if o.Stage != "Finished" || o.StartedAt == nil || !o.StartedAt.Equal(start) || o.FinishedAt == nil || !o.FinishedAt.Equal(finish) {
		t.Fatalf("missing main container timing: %+v", o)
	}
}
