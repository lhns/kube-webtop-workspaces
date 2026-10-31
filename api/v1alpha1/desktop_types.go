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
// +kubebuilder:validation:Enum=user;idle;suspend;kube-stop;exit;admin;ldap
type StopReason string

const (
	StopUser     StopReason = "user"
	StopIdle     StopReason = "idle"
	StopSuspend  StopReason = "suspend"
	StopKubeStop StopReason = "kube-stop"
	StopExit     StopReason = "exit"
	StopAdmin    StopReason = "admin"
	StopLDAP     StopReason = "ldap"
)

// Stage ids the gateway also reads (docs/contract.md, section 3.4).
const (
	StageCreatingContainer = "CreatingContainer"
	StageAttachingVolume   = "AttachingVolume"
	StagePullingImage      = "PullingImage"
)

// Condition types and reasons.
const (
	ConditionAuthorized = "Authorized"
	ConditionReady      = "Ready"

	ReasonMember       = "Member"
	ReasonLDAPDisabled = "LDAPDisabled"
	ReasonNotMember    = "NotMember"
	ReasonLDAPError    = "LDAPError"
	ReasonInvalidName  = "InvalidName"
)

// DesktopSpec is the desired state of a Desktop.
type DesktopSpec struct {
	// User is the user ID exactly as the auth header and LDAP have it.
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
	// Message gives detail for Error or an unauthorized desktop.
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
