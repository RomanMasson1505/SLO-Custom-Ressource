/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// ServiceLevelObjectiveSpec is the desired state: what the user wants to guarantee.
type ServiceLevelObjectiveSpec struct {
	// Description is a human-readable summary of the SLO.
	// +optional
	Description string `json:"description,omitempty"`

	// Objective is the target in percent, e.g. "99.9".
	// We keep it as a string on purpose: floats lose precision (99.9 -> 99.90000001)
	// and don't round-trip cleanly through JSON. We parse it with ParseFloat when needed.
	// +kubebuilder:validation:Required
	Objective string `json:"objective"`

	// Window is the rolling evaluation window, in Prometheus duration format (e.g. "30d").
	// +kubebuilder:default:="30d"
	Window string `json:"window,omitempty"`

	// SLI describes what we actually measure.
	SLI SLISpec `json:"sli"`

	// Enforcement is optional opt-in behaviour (freeze deployments when the budget is gone).
	// +optional
	Enforcement *EnforcementSpec `json:"enforcement,omitempty"`
}

// SLISpec describes the Service Level Indicator: the signal we watch in Prometheus.
type SLISpec struct {
	// Type is the kind of SLI. Kept as an enum so the API server rejects typos.
	// +kubebuilder:validation:Enum=availability;latency
	Type string `json:"type"`

	// TotalQuery is the PromQL for the denominator (all events).
	// It may contain a {{.Window}} placeholder that the operator substitutes per burn-rate window,
	// e.g. sum(rate(http_requests_total{service="checkout"}[{{.Window}}])).
	TotalQuery string `json:"totalQuery"`

	// ErrorQuery is the PromQL for the numerator (bad events), same {{.Window}} rules,
	// e.g. sum(rate(http_requests_total{service="checkout",code=~"5.."}[{{.Window}}])).
	ErrorQuery string `json:"errorQuery"`
}

// EnforcementSpec is the opt-in "deployment freeze" configuration.
type EnforcementSpec struct {
	// FreezeDeployments, when true, labels the matching Deployments once the budget is exhausted.
	FreezeDeployments bool `json:"freezeDeployments,omitempty"`

	// Selector picks the target Deployments in the SLO's namespace.
	// The validating webhook requires it when FreezeDeployments is true.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`
}

// ServiceLevelObjectiveStatus is the observed state: what the controller measured and decided.
type ServiceLevelObjectiveStatus struct {
	// ErrorBudgetRemaining is the remaining error budget in percent (0-100).
	// String for the same precision reason as Objective.
	// +optional
	ErrorBudgetRemaining string `json:"errorBudgetRemaining,omitempty"`

	// CurrentBurnRate is the latest 1h burn rate (how many times faster than "normal" we burn budget).
	// +optional
	CurrentBurnRate string `json:"currentBurnRate,omitempty"`

	// Phase is a one-word health summary: Healthy | Warning | Exhausted | Unknown.
	// +optional
	Phase string `json:"phase,omitempty"`

	// Conditions are the standard, machine-readable state lines:
	// Ready, RulesGenerated, PrometheusReachable, BudgetHealthy.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the spec generation the controller last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// LastEvaluationTime is when Prometheus was last queried successfully.
	// +optional
	LastEvaluationTime *metav1.Time `json:"lastEvaluationTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=slo
// +kubebuilder:printcolumn:name="Objective",type=string,JSONPath=`.spec.objective`
// +kubebuilder:printcolumn:name="Window",type=string,JSONPath=`.spec.window`
// +kubebuilder:printcolumn:name="Budget-Remaining",type=string,JSONPath=`.status.errorBudgetRemaining`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ServiceLevelObjective is the Schema for the servicelevelobjectives API
type ServiceLevelObjective struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of ServiceLevelObjective
	// +required
	Spec ServiceLevelObjectiveSpec `json:"spec"`

	// status defines the observed state of ServiceLevelObjective
	// +optional
	Status ServiceLevelObjectiveStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ServiceLevelObjectiveList contains a list of ServiceLevelObjective
type ServiceLevelObjectiveList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ServiceLevelObjective `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &ServiceLevelObjective{}, &ServiceLevelObjectiveList{})
		return nil
	})
}
