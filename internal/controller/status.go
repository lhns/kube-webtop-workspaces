// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"slices"
	"sort"

	corev1 "k8s.io/api/core/v1"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
	"github.com/lhns/kube-webtop-workspaces/internal/lifecycle"
	"github.com/lhns/kube-webtop-workspaces/internal/names"
)

// Stages of docs/contract.md, section 3.4.
const (
	stageStopped    = "Stopped"
	stageStopping   = "Stopping"
	stageScheduling = "Scheduling"
	stageBooting    = "Booting"
	stageReady      = "Ready"
	stageError      = "Error"
)

// Boot progress before the agent reports its own.
const (
	progressScheduling = 5
	progressCreating   = 15
	progressBooting    = 25
	progressAgentReady = 95
)

// metricStage is the stage as a metric label: the agent's free-text stages become Booting, so
// what the pod reports never sets label values.
func metricStage(stage string) string {
	switch stage {
	case stageStopped, stageStopping, stageScheduling, v1alpha1.StageCreatingContainer, v1alpha1.StageAttachingVolume, v1alpha1.StagePullingImage, stageBooting, stageReady, stageError:
		return stage
	}
	if slices.Contains(waitingErrors, stage) {
		return stage
	}
	return stageBooting
}

// waitingErrors are container waiting reasons that make the desktop phase Error.
var waitingErrors = []string{"ErrImagePull", "ImagePullBackOff", "CrashLoopBackOff", "CreateContainerConfigError"}

// statusInput is everything computeStatus needs; it reads no client.
type statusInput struct {
	// Run is true when the desktop is supposed to run: powerState On and allowed to start or keep running.
	Run bool
	// ErrMsg is set for an invalid name or a foreign PVC.
	ErrMsg string
	// Note is shown with an Off desktop that is On but not allowed to run (unauthorized).
	Note string
	// PodExists is true when any pod of the desktop exists, terminating ones included.
	PodExists bool
	// Pod is the newest non-terminating pod, or nil.
	Pod               *corev1.Pod
	RequireAgentReady bool
	// AgentReady, AgentReported, AgentStage and AgentProgress are what the agent of Pod reported.
	AgentReady    bool
	AgentReported bool
	AgentStage    string
	AgentProgress int32
	// AttachFailing and Pulling come from the events of Pod; they refine the waiting container.
	AttachFailing bool
	Pulling       bool
}

type statusResult struct {
	Phase    v1alpha1.Phase
	Stage    string
	Message  string
	Progress int32
}

func computeStatus(in statusInput) statusResult {
	if in.ErrMsg != "" {
		return statusResult{Phase: v1alpha1.PhaseError, Stage: stageError, Message: in.ErrMsg}
	}
	if !in.Run {
		if in.PodExists {
			return statusResult{Phase: v1alpha1.PhaseStopping, Stage: stageStopping, Message: in.Note}
		}
		return statusResult{Phase: v1alpha1.PhaseOff, Stage: stageStopped, Message: in.Note}
	}
	starting := func(stage string, progress int32, msg string) statusResult {
		return statusResult{Phase: v1alpha1.PhaseStarting, Stage: stage, Progress: progress, Message: msg}
	}
	pod := in.Pod
	if pod == nil {
		return starting(stageScheduling, progressScheduling, "")
	}
	if c := podCondition(pod, corev1.PodScheduled); c != nil && c.Status == corev1.ConditionFalse && c.Reason == corev1.PodReasonUnschedulable {
		return starting(stageScheduling, progressScheduling, c.Message)
	}
	cs := containerStatus(pod)
	if cs == nil || cs.State.Waiting != nil {
		if cs != nil && slices.Contains(waitingErrors, cs.State.Waiting.Reason) {
			return statusResult{Phase: v1alpha1.PhaseError, Stage: cs.State.Waiting.Reason, Message: cs.State.Waiting.Message}
		}
		switch {
		case in.Pulling: // volumes are attached before the kubelet pulls, so a running pull wins
			return starting(v1alpha1.StagePullingImage, progressCreating, "")
		case in.AttachFailing:
			return starting(v1alpha1.StageAttachingVolume, progressCreating, "")
		}
		return starting(v1alpha1.StageCreatingContainer, progressCreating, "")
	}
	if cs.State.Running == nil {
		return starting(stageBooting, progressBooting, "") // terminated, restart pending
	}
	if podReady(pod) && (in.AgentReady || !in.RequireAgentReady) {
		return statusResult{Phase: v1alpha1.PhaseRunning, Stage: stageReady, Progress: 100}
	}
	if in.AgentReady {
		return starting(stageBooting, progressAgentReady, "")
	}
	if in.AgentReported {
		stage := in.AgentStage
		if stage == "" {
			stage = stageBooting
		}
		progress := in.AgentProgress
		if progress == 0 {
			progress = progressBooting
		}
		return starting(stage, progress, "")
	}
	return starting(stageBooting, progressBooting, "")
}

func podCondition(pod *corev1.Pod, t corev1.PodConditionType) *corev1.PodCondition {
	for i := range pod.Status.Conditions {
		if pod.Status.Conditions[i].Type == t {
			return &pod.Status.Conditions[i]
		}
	}
	return nil
}

func podReady(pod *corev1.Pod) bool {
	c := podCondition(pod, corev1.PodReady)
	return c != nil && c.Status == corev1.ConditionTrue
}

func containerStatus(pod *corev1.Pod) *corev1.ContainerStatus {
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == names.ContainerName {
			return &pod.Status.ContainerStatuses[i]
		}
	}
	return nil
}

// newestPod returns the newest pod that is not terminating, or nil.
func newestPod(pods []corev1.Pod) *corev1.Pod {
	live := make([]*corev1.Pod, 0, len(pods))
	for i := range pods {
		if pods[i].DeletionTimestamp.IsZero() {
			live = append(live, &pods[i])
		}
	}
	if len(live) == 0 {
		return nil
	}
	sort.Slice(live, func(i, j int) bool {
		a, b := live[i], live[j]
		if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
			return b.CreationTimestamp.Before(&a.CreationTimestamp)
		}
		return a.Name > b.Name
	})
	return live[0]
}

// cleanExit reports whether pod's desktop container ended with one of the clean exit codes
// since the last start, and that is not a reboot or an already recorded stop
// (docs/contract.md, section 3.2 step 7). The caller checks powerState=On.
func cleanExit(d *v1alpha1.Desktop, pod *corev1.Pod, codes []int32) bool {
	if pod == nil || d.Status.LastStart == nil {
		return false
	}
	if lifecycle.StopRecorded(d) {
		return false
	}
	cs := containerStatus(pod)
	if cs == nil {
		return false
	}
	for _, t := range []*corev1.ContainerStateTerminated{cs.State.Terminated, cs.LastTerminationState.Terminated} {
		if t == nil || !slices.Contains(codes, t.ExitCode) {
			continue
		}
		if !t.FinishedAt.After(d.Status.LastStart.Time) {
			continue
		}
		if rr := d.Spec.RestartRequestedAt; rr != nil && rr.After(t.StartedAt.Time) {
			continue // a reboot
		}
		return true
	}
	return false
}

// deployPlan says how the Deployment is written (docs/contract.md, section 3.2 step 4).
type deployPlan struct {
	// WriteTemplate writes the full Deployment: template, hash annotation and replicas.
	WriteTemplate bool
	// StampRestart stamps restarted-at on the pod template, which rolls out a running pod.
	StampRestart bool
	// ConsumeRestart sets status.observedRestartAt.
	ConsumeRestart bool
	// SetLastStart sets status.lastStart to now.
	SetLastStart bool
}

// planDeploy decides between a full write and a replicas-only patch. A running desktop
// (cur > 0) only gets its template written for a restart request. A restart request met while
// the desktop is not running, or being stopped, is consumed without a rollout.
func planDeploy(exists bool, cur, want int32, restartPending bool) deployPlan {
	switch {
	case !exists || cur == 0:
		return deployPlan{WriteTemplate: true, StampRestart: restartPending, ConsumeRestart: restartPending, SetLastStart: want > 0}
	case restartPending && want > 0:
		return deployPlan{WriteTemplate: true, StampRestart: true, ConsumeRestart: true, SetLastStart: true}
	case restartPending:
		return deployPlan{ConsumeRestart: true}
	}
	return deployPlan{}
}
