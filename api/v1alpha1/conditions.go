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

// Phases are the one-word summaries surfaced in status.phase.
// Using constants keeps every reference spelled the same way.
const (
	// PhaseHealthy means more than 25% of the error budget is left.
	PhaseHealthy = "Healthy"
	// PhaseWarning means 25% or less of the budget is left.
	PhaseWarning = "Warning"
	// PhaseExhausted means the budget is gone (0% or below).
	PhaseExhausted = "Exhausted"
	// PhaseUnknown means we couldn't evaluate (e.g. Prometheus was unreachable).
	PhaseUnknown = "Unknown"
)

// Condition types published in status.conditions.
const (
	// CondReady is the overall "everything is fine" summary condition.
	CondReady = "Ready"
	// CondRulesGenerated is True once the PrometheusRule has been applied.
	CondRulesGenerated = "RulesGenerated"
	// CondPrometheusReachable is True when the last Prometheus query succeeded.
	CondPrometheusReachable = "PrometheusReachable"
	// CondBudgetHealthy is True while the budget is not exhausted.
	CondBudgetHealthy = "BudgetHealthy"
)
