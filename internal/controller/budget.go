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

package controller

import srev1alpha1 "github.com/RomanMasson1505/SLO-Custom-Ressource/api/v1alpha1"

// These helpers are pure arithmetic (no cluster, no network), so they are
// trivially unit-testable on their own.

// computePhase maps a remaining-budget percentage to a one-word health phase.
func computePhase(remainingPercent float64) string {
	switch {
	case remainingPercent <= 0:
		return srev1alpha1.PhaseExhausted
	case remainingPercent <= 25:
		return srev1alpha1.PhaseWarning
	default:
		return srev1alpha1.PhaseHealthy
	}
}

// clamp bounds v into [lo, hi].
func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
