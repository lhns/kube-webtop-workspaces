// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PowerState is the desired power state of a desktop.
// +kubebuilder:validation:Enum=On;Off
type PowerState string

const (
	PowerOn  PowerState = "On"
	PowerOff PowerState = "Off"
)

// Phase summarises a desktop's state. The gateway proxies only in Running.
// +kubebuilder:validation:Enum=Off;Starting;Running;Stopping;Error
type Phase string

const (
	PhaseOff      Phase = "Off"
	PhaseStarting Phase = "Starting"
	PhaseRunning  Phase = "Running"
	PhaseStopping Phase = "Stopping"
	PhaseError    Phase = "Error"
)

// StopReason says why a desktop stopped. See docs/contract.md, "Stop reasons".
// +kubebuilder:validation:Enum=user;idle;suspend;kube-stop;exit;admin;unseen;error
type StopReason string

const (
	StopUser     StopReason = "user"
	StopIdle     StopReason = "idle"
	StopSuspend  StopReason = "suspend"
	StopKubeStop StopReason = "kube-stop"
	StopExit     StopReason = "exit"
	StopAdmin    StopReason = "admin"
	StopUnseen   StopReason = "unseen"
	StopError    StopReason = "error"
)

// Stage ids the gateway also reads (docs/contract.md, section 3.4).
const (
	StageCreatingContainer = "CreatingContainer"
	StageAttachingVolume   = "AttachingVolume"
	StagePullingImage      = "PullingImage"
	StageStoppingPrevious  = "StoppingPrevious"
)

// Condition types and reasons.
const (
	ConditionReady     = "Ready"
	ConditionLoginName = "LoginName"

	// LoginName reasons: the desktop user has the desktop's name, or fell back to "user" because
	// the name is not a valid login name or is an account of the image.
	ReasonDesktopName      = "DesktopName"
	ReasonInvalidLoginName = "InvalidLoginName"
	ReasonReservedName     = "ReservedName"
	// RenameFailed: usermod failed at activation; status.loginNameError says why.
	ReasonRenameFailed = "RenameFailed"
)

// DesktopSpec is the desired state of a Desktop.
type DesktopSpec struct {
	// User is the user ID exactly as the auth header has it.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="user is immutable"
	User string `json:"user"`

	// PowerState is the desired power state.
	// +kubebuilder:default=Off
	// +optional
	PowerState PowerState `json:"powerState,omitempty"`

	// RestartRequestedAt requests a restart when newer than status.observedRestartAt.
	// A restart also applies a pending update.
	// +optional
	RestartRequestedAt *metav1.Time `json:"restartRequestedAt,omitempty"`

	// LastSeen is when the gateway last served the owner, at most hourly. desktop.stopAfterUnseen
	// stops a desktop older than that. The gateway may not write status, so it lives in spec.
	// +optional
	LastSeen *metav1.Time `json:"lastSeen,omitempty"`

	// DisplayName is the owner's name from the login proxy's header, as the gateway last saw it.
	// A start copies it to status.displayName; a running pod never sees a change.
	// +kubebuilder:validation:MaxLength=512
	// +optional
	DisplayName string `json:"displayName,omitempty"`
}

// LastStop records the most recent recorded stop. Microsecond precision: compared with LastStart.
type LastStop struct {
	Time   metav1.MicroTime `json:"time"`
	Reason StopReason       `json:"reason"`
}

// ImagePull is what the controller knows about pulling the desktop image, for the boot page's
// download estimate.
type ImagePull struct {
	// Image is the image reference the fields below are about.
	Image string `json:"image"`
	// StartedAt is when the kubelet started the pull that is still running.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// LastSizeBytes is the size the kubelet reported for the last completed pull of Image.
	// +optional
	LastSizeBytes int64 `json:"lastSizeBytes,omitempty"`
	// LastDurationSeconds is how long that pull took.
	// +optional
	LastDurationSeconds int64 `json:"lastDurationSeconds,omitempty"`
}

// SoftwareRestore is the progress of restoring the user's software after an image update, as the
// agent reported it. It never blocks Ready.
type SoftwareRestore struct {
	Active bool `json:"active"`
	// Done and Total count steps: each reinstalled profile entry plus the home-manager switch.
	Done   int32 `json:"done"`
	Total  int32 `json:"total"`
	Failed bool  `json:"failed,omitempty"`
}

// DesktopStatus is the observed state of a Desktop.
type DesktopStatus struct {
	// +optional
	Phase Phase `json:"phase,omitempty"`
	// Stage is a human-readable current step.
	// +optional
	Stage string `json:"stage,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +optional
	BootProgress int32 `json:"bootProgress,omitempty"`
	// ImagePull records image pulls of the desktop pod; kept while the desktop is off.
	// +optional
	ImagePull *ImagePull `json:"imagePull,omitempty"`
	// Message gives detail for Error or a desktop that cannot start.
	// +optional
	Message string `json:"message,omitempty"`
	// URL is the proxy target of the gateway.
	// +optional
	URL string `json:"url,omitempty"`
	// +optional
	PodName string `json:"podName,omitempty"`
	// +optional
	PodUID string `json:"podUID,omitempty"`
	// AgentReady is true once the agent of PodUID reported ready.
	// +optional
	AgentReady bool `json:"agentReady,omitempty"`
	// LastStart is when powerState last went On or a restart was applied.
	// +optional
	LastStart *metav1.MicroTime `json:"lastStart,omitempty"`
	// +optional
	LastStop *LastStop `json:"lastStop,omitempty"`
	// +optional
	LastReportTime *metav1.Time `json:"lastReportTime,omitempty"`
	// AppliedTemplateHash is the pod template hash on the Deployment.
	// +optional
	AppliedTemplateHash string `json:"appliedTemplateHash,omitempty"`
	// UpdatePending is true while a running desktop's template is out of date.
	// +optional
	UpdatePending bool `json:"updatePending,omitempty"`
	// ObservedRestartAt is the last spec.restartRequestedAt the controller applied.
	// +optional
	ObservedRestartAt *metav1.Time `json:"observedRestartAt,omitempty"`
	// +optional
	ConnectedClients int32 `json:"connectedClients,omitempty"`
	// +optional
	IdleSeconds int64 `json:"idleSeconds,omitempty"`
	// Asleep is true while Sleep has the session frozen; the pod keeps running and the next
	// web client wakes it. Reset with the pod.
	// +optional
	Asleep bool `json:"asleep,omitempty"`
	// LastLogout is when the user logged out of the session without a shutdown, as the agent
	// reported it. Reset with the pod.
	// +optional
	LastLogout *metav1.Time `json:"lastLogout,omitempty"`
	// LastLock is when the user used Lock, as the agent reported it, if the user's choice (default
	// gateway.ssoLogoutDefaults.onLock) says to sign out at the provider, gateway.lockLogsOut is on and
	// gateway.logoutUrl is set. Lock does not end the session. Reset with the pod.
	// +optional
	LastLock *metav1.Time `json:"lastLock,omitempty"`
	// LoginName is the desktop user's login name, as the agent reported it: the desktop name, or
	// "user" when that is not usable (condition LoginName). Kept while off.
	// +optional
	LoginName string `json:"loginName,omitempty"`
	// SoftwareRestore is set while, or after, the software is restored after an image update.
	// Reset with the pod.
	// +optional
	SoftwareRestore *SoftwareRestore `json:"softwareRestore,omitempty"`
	// LoginNameError is why the image could not rename the user to the desktop name, as the agent
	// reported it. Empty when it could.
	// +kubebuilder:validation:MaxLength=512
	// +optional
	LoginNameError string `json:"loginNameError,omitempty"`
	// HomePaths are the paths the user's home is mounted at, as the agent reported them. Kept while off.
	// +kubebuilder:validation:MaxItems=16
	// +optional
	HomePaths []string `json:"homePaths,omitempty"`
	// DisplayName is the display name the current or last pod was started with.
	// +optional
	DisplayName string `json:"displayName,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Desktop is one user's persistent desktop.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=dt
// +kubebuilder:printcolumn:name="User",type=string,JSONPath=`.spec.user`
// +kubebuilder:printcolumn:name="Power",type=string,JSONPath=`.spec.powerState`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Stage",type=string,JSONPath=`.status.stage`
// +kubebuilder:printcolumn:name="Clients",type=integer,JSONPath=`.status.connectedClients`
// +kubebuilder:printcolumn:name="Last Seen",type=date,JSONPath=`.spec.lastSeen`
// +kubebuilder:printcolumn:name="Asleep",type=boolean,JSONPath=`.status.asleep`
// +kubebuilder:printcolumn:name="Update",type=boolean,JSONPath=`.status.updatePending`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Desktop struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DesktopSpec   `json:"spec"`
	Status DesktopStatus `json:"status,omitempty"`
}

// DesktopList is a list of Desktops.
// +kubebuilder:object:root=true
type DesktopList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Desktop `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Desktop{}, &DesktopList{})
}
