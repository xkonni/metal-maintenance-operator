// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// FirmwareUpdateSetUpdateStrategy controls rollout concurrency for a FirmwareUpdateSet.
// Future knobs (pauseOnFailure, wave sizing) will be added here.
type FirmwareUpdateSetUpdateStrategy struct {
	// MaxConcurrent limits the number of FirmwareUpdate children in InProgress state at once.
	// Zero means unlimited.
	// +optional
	MaxConcurrent int32 `json:"maxConcurrent,omitempty"`
}

// FirmwareUpdateSetSpec defines the desired state of FirmwareUpdateSet.
type FirmwareUpdateSetSpec struct {
	// ServerSelector specifies a label selector to identify the servers to apply the firmware update on.
	// +required
	ServerSelector metav1.LabelSelector `json:"serverSelector"`

	// FirmwareUpdateTemplate defines the template for the FirmwareUpdate resource to be applied to the servers.
	// +optional
	FirmwareUpdateTemplate FirmwareUpdateTemplate `json:"firmwareUpdateTemplate,omitempty"`

	// UpdateStrategy controls how FirmwareUpdate children are rolled out.
	// +optional
	UpdateStrategy FirmwareUpdateSetUpdateStrategy `json:"updateStrategy,omitempty"`
}

// FirmwareUpdateSetStatus defines the observed state of FirmwareUpdateSet.
type FirmwareUpdateSetStatus struct {
	// MatchingServers is the number of servers matching the selector.
	// +optional
	MatchingServers int32 `json:"matchingServers,omitempty"`

	// PendingFirmwareUpdate is the number of FirmwareUpdate resources in a pending state.
	// +optional
	PendingFirmwareUpdate int32 `json:"pendingFirmwareUpdate,omitempty"`

	// InProgressFirmwareUpdate is the number of FirmwareUpdate resources currently in progress.
	// +optional
	InProgressFirmwareUpdate int32 `json:"inProgressFirmwareUpdate,omitempty"`

	// CompletedFirmwareUpdate is the number of FirmwareUpdate resources that completed successfully.
	// +optional
	CompletedFirmwareUpdate int32 `json:"completedFirmwareUpdate,omitempty"`

	// FailedFirmwareUpdate is the number of FirmwareUpdate resources that failed.
	// +optional
	FailedFirmwareUpdate int32 `json:"failedFirmwareUpdate,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=fwus
// +kubebuilder:printcolumn:name="MatchingServers",type=integer,JSONPath=`.status.matchingServers`
// +kubebuilder:printcolumn:name="Pending",type=integer,JSONPath=`.status.pendingFirmwareUpdate`
// +kubebuilder:printcolumn:name="InProgress",type=integer,JSONPath=`.status.inProgressFirmwareUpdate`
// +kubebuilder:printcolumn:name="Completed",type=integer,JSONPath=`.status.completedFirmwareUpdate`
// +kubebuilder:printcolumn:name="Failed",type=integer,JSONPath=`.status.failedFirmwareUpdate`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// FirmwareUpdateSet is the Schema for the firmwareupdatesets API.
type FirmwareUpdateSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FirmwareUpdateSetSpec   `json:"spec,omitempty"`
	Status FirmwareUpdateSetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FirmwareUpdateSetList contains a list of FirmwareUpdateSet.
type FirmwareUpdateSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FirmwareUpdateSet `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FirmwareUpdateSet{}, &FirmwareUpdateSetList{})
}
