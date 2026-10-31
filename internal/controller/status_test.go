// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
)

func mkPod(mod func(*corev1.Pod)) *corev1.Pod {
	p := &corev1.Pod{Status: corev1.PodStatus{
		Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionTrue}},
	}}
	if mod != nil {
		mod(p)
	}
	return p
}

func containerState(s corev1.ContainerState) func(*corev1.Pod) {
	return func(p *corev1.Pod) {
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "desktop", State: s}}
	}
}

func running() corev1.ContainerState {
	return corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
}

func ready(p *corev1.Pod) {
	p.Status.Conditions = append(p.Status.Conditions, corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue})
}

func TestComputeStatus(t *testing.T) {
	waiting := func(reason string) *corev1.Pod {
		return mkPod(containerState(corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: "m"}}))
	}
	runningPod := mkPod(containerState(running()))
	readyPod := mkPod(func(p *corev1.Pod) { containerState(running())(p); ready(p) })

	tests := []struct {
		name string
		in   statusInput
		want statusResult
	}{
		{"error message wins", statusInput{Run: true, ErrMsg: "bad", Pod: readyPod},
			statusResult{Phase: "Error", Stage: "Error", Message: "bad"}},
		{"off no pod", statusInput{}, statusResult{Phase: "Off", Stage: "Stopped"}},
		{"off with note", statusInput{Note: "nope"}, statusResult{Phase: "Off", Stage: "Stopped", Message: "nope"}},
		{"off pod exists", statusInput{PodExists: true, Pod: readyPod}, statusResult{Phase: "Stopping", Stage: "Stopping"}},
		{"off only terminating pod", statusInput{PodExists: true}, statusResult{Phase: "Stopping", Stage: "Stopping"}},
		{"on no pod", statusInput{Run: true}, statusResult{Phase: "Starting", Stage: "Scheduling", Progress: 5}},
		{"unschedulable", statusInput{Run: true, Pod: mkPod(func(p *corev1.Pod) {
			p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable", Message: "no nodes"}}
		})}, statusResult{Phase: "Starting", Stage: "Scheduling", Progress: 5, Message: "no nodes"}},
		{"no container status yet", statusInput{Run: true, Pod: mkPod(nil)},
			statusResult{Phase: "Starting", Stage: "CreatingContainer", Progress: 15}},
		{"container creating", statusInput{Run: true, Pod: waiting("ContainerCreating")},
			statusResult{Phase: "Starting", Stage: "CreatingContainer", Progress: 15}},
		{"pod initializing", statusInput{Run: true, Pod: waiting("PodInitializing")},
			statusResult{Phase: "Starting", Stage: "CreatingContainer", Progress: 15}},
		{"waiting, pull running", statusInput{Run: true, Pod: waiting("ContainerCreating"), Pulling: true},
			statusResult{Phase: "Starting", Stage: "PullingImage", Progress: 15}},
		{"waiting, volume attach failing", statusInput{Run: true, Pod: waiting("ContainerCreating"), AttachFailing: true},
			statusResult{Phase: "Starting", Stage: "AttachingVolume", Progress: 15}},
		{"pull wins over a stale attach failure", statusInput{Run: true, Pod: waiting("ContainerCreating"), Pulling: true, AttachFailing: true},
			statusResult{Phase: "Starting", Stage: "PullingImage", Progress: 15}},
		{"no container status yet, pulling", statusInput{Run: true, Pod: mkPod(nil), Pulling: true},
			statusResult{Phase: "Starting", Stage: "PullingImage", Progress: 15}},
		{"pull error beats pulling", statusInput{Run: true, Pod: waiting("ErrImagePull"), Pulling: true},
			statusResult{Phase: "Error", Stage: "ErrImagePull", Message: "m"}},
		{"running ignores stale pull events", statusInput{Run: true, Pod: runningPod, RequireAgentReady: true, Pulling: true, AttachFailing: true},
			statusResult{Phase: "Starting", Stage: "Booting", Progress: 25}},
		{"ErrImagePull", statusInput{Run: true, Pod: waiting("ErrImagePull")},
			statusResult{Phase: "Error", Stage: "ErrImagePull", Message: "m"}},
		{"ImagePullBackOff", statusInput{Run: true, Pod: waiting("ImagePullBackOff")},
			statusResult{Phase: "Error", Stage: "ImagePullBackOff", Message: "m"}},
		{"CrashLoopBackOff", statusInput{Run: true, Pod: waiting("CrashLoopBackOff")},
			statusResult{Phase: "Error", Stage: "CrashLoopBackOff", Message: "m"}},
		{"CreateContainerConfigError", statusInput{Run: true, Pod: waiting("CreateContainerConfigError")},
			statusResult{Phase: "Error", Stage: "CreateContainerConfigError", Message: "m"}},
		{"running, no report", statusInput{Run: true, Pod: runningPod, RequireAgentReady: true},
			statusResult{Phase: "Starting", Stage: "Booting", Progress: 25}},
		{"agent reported stage", statusInput{Run: true, Pod: runningPod, RequireAgentReady: true, AgentReported: true, AgentStage: "Starting Plasma", AgentProgress: 60},
			statusResult{Phase: "Starting", Stage: "Starting Plasma", Progress: 60}},
		{"agent reported, no stage text", statusInput{Run: true, Pod: runningPod, RequireAgentReady: true, AgentReported: true},
			statusResult{Phase: "Starting", Stage: "Booting", Progress: 25}},
		{"pod ready but agent not", statusInput{Run: true, Pod: readyPod, RequireAgentReady: true},
			statusResult{Phase: "Starting", Stage: "Booting", Progress: 25}},
		{"pod ready, agent not required", statusInput{Run: true, Pod: readyPod},
			statusResult{Phase: "Running", Stage: "Ready", Progress: 100}},
		{"pod ready and agent ready", statusInput{Run: true, Pod: readyPod, RequireAgentReady: true, AgentReady: true},
			statusResult{Phase: "Running", Stage: "Ready", Progress: 100}},
		{"agent ready, pod not ready", statusInput{Run: true, Pod: runningPod, RequireAgentReady: true, AgentReady: true},
			statusResult{Phase: "Starting", Stage: "Booting", Progress: 95}},
		{"container terminated, restarting", statusInput{Run: true, Pod: mkPod(containerState(corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}}))},
			statusResult{Phase: "Starting", Stage: "Booting", Progress: 25}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := computeStatus(tc.in); got != tc.want {
				t.Fatalf("got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestMetricStage(t *testing.T) {
	for in, want := range map[string]string{
		"Scheduling": "Scheduling", "Ready": "Ready", "PullingImage": "PullingImage", "AttachingVolume": "AttachingVolume", "ImagePullBackOff": "ImagePullBackOff",
		"Starting Plasma": "Booting", "": "Booting",
	} {
		if got := metricStage(in); got != want {
			t.Errorf("metricStage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewestPod(t *testing.T) {
	now := metav1.Now()
	old := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "old", CreationTimestamp: metav1.NewTime(now.Add(-time.Hour))}}
	newer := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "new", CreationTimestamp: now}}
	dying := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "dying", CreationTimestamp: metav1.NewTime(now.Add(time.Hour)), DeletionTimestamp: &now}}
	if p := newestPod([]corev1.Pod{old, dying, newer}); p == nil || p.Name != "new" {
		t.Fatalf("got %v", p)
	}
	if newestPod([]corev1.Pod{dying}) != nil || newestPod(nil) != nil {
		t.Fatal("no live pod must give nil")
	}
}

func TestCleanExit(t *testing.T) {
	start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	micro := func(t time.Time) *metav1.MicroTime { m := metav1.NewMicroTime(t); return &m }
	term := func(code int32, started, finished time.Time) *corev1.ContainerStateTerminated {
		return &corev1.ContainerStateTerminated{ExitCode: code, StartedAt: metav1.NewTime(started), FinishedAt: metav1.NewTime(finished)}
	}
	podWith := func(cur, last *corev1.ContainerStateTerminated) *corev1.Pod {
		return &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name: "desktop", State: corev1.ContainerState{Terminated: cur}, LastTerminationState: corev1.ContainerState{Terminated: last},
		}}}}
	}
	desktop := func(mod func(*v1alpha1.Desktop)) *v1alpha1.Desktop {
		d := &v1alpha1.Desktop{Status: v1alpha1.DesktopStatus{LastStart: micro(start)}}
		if mod != nil {
			mod(d)
		}
		return d
	}
	after := start.Add(time.Minute)
	codes := []int32{0}

	tests := []struct {
		name string
		d    *v1alpha1.Desktop
		pod  *corev1.Pod
		want bool
	}{
		{"current state clean exit", desktop(nil), podWith(term(0, start, after), nil), true},
		{"last state clean exit", desktop(nil), podWith(nil, term(0, start, after)), true},
		{"crash exit code", desktop(nil), podWith(term(1, start, after), nil), false},
		{"finished before lastStart", desktop(nil), podWith(term(0, start.Add(-time.Hour), start.Add(-time.Minute)), nil), false},
		{"no termination", desktop(nil), podWith(nil, nil), false},
		{"no pod", desktop(nil), nil, false},
		{"no lastStart", desktop(func(d *v1alpha1.Desktop) { d.Status.LastStart = nil }), podWith(term(0, start, after), nil), false},
		{"reboot newer than container start", desktop(func(d *v1alpha1.Desktop) {
			d.Spec.RestartRequestedAt = &metav1.Time{Time: start.Add(30 * time.Second)}
		}), podWith(term(0, start, after), nil), false},
		{"old restart request does not exclude", desktop(func(d *v1alpha1.Desktop) {
			d.Spec.RestartRequestedAt = &metav1.Time{Time: start.Add(-time.Hour)}
		}), podWith(term(0, start, after), nil), true},
		{"stop already recorded", desktop(func(d *v1alpha1.Desktop) {
			d.Status.LastStop = &v1alpha1.LastStop{Time: *micro(start.Add(30 * time.Second)), Reason: "idle"}
		}), podWith(term(0, start, after), nil), false},
		{"older stop does not exclude", desktop(func(d *v1alpha1.Desktop) {
			d.Status.LastStop = &v1alpha1.LastStop{Time: *micro(start.Add(-time.Hour)), Reason: "idle"}
		}), podWith(term(0, start, after), nil), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := cleanExit(tc.d, tc.pod, codes); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
	if !cleanExit(desktop(nil), podWith(term(143, start, after), nil), []int32{0, 143}) {
		t.Fatal("configured exit codes must count")
	}
}

func TestPlanDeploy(t *testing.T) {
	tests := []struct {
		name           string
		exists         bool
		cur, want      int32
		restartPending bool
		plan           deployPlan
	}{
		{"create off", false, 0, 0, false, deployPlan{WriteTemplate: true}},
		{"create on", false, 0, 1, false, deployPlan{WriteTemplate: true, SetLastStart: true}},
		{"off stays off: template applied", true, 0, 0, false, deployPlan{WriteTemplate: true}},
		{"0 to 1", true, 0, 1, false, deployPlan{WriteTemplate: true, SetLastStart: true}},
		{"running stays running: replicas only", true, 1, 1, false, deployPlan{}},
		{"running to off: replicas only", true, 1, 0, false, deployPlan{}},
		{"restart of a running desktop", true, 1, 1, true, deployPlan{WriteTemplate: true, StampRestart: true, ConsumeRestart: true, SetLastStart: true}},
		{"restart while stopping is only consumed", true, 1, 0, true, deployPlan{ConsumeRestart: true}},
		{"restart while off is consumed and stamped", true, 0, 0, true, deployPlan{WriteTemplate: true, StampRestart: true, ConsumeRestart: true}},
		{"restart on a 0 to 1 start", true, 0, 1, true, deployPlan{WriteTemplate: true, StampRestart: true, ConsumeRestart: true, SetLastStart: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := planDeploy(tc.exists, tc.cur, tc.want, tc.restartPending); got != tc.plan {
				t.Fatalf("got %+v, want %+v", got, tc.plan)
			}
		})
	}
}
