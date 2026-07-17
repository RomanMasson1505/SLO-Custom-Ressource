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

import (
	"testing"

	srev1alpha1 "github.com/RomanMasson1505/SLO-Custom-Ressource/api/v1alpha1"
)

func TestComputePhase(t *testing.T) {
	cases := []struct {
		name      string
		remaining float64
		want      string
	}{
		{"full budget", 100, srev1alpha1.PhaseHealthy},
		{"just above warning", 25.1, srev1alpha1.PhaseHealthy},
		{"at warning boundary", 25, srev1alpha1.PhaseWarning},
		{"low but positive", 0.1, srev1alpha1.PhaseWarning},
		{"exactly zero", 0, srev1alpha1.PhaseExhausted},
		{"negative (clamped upstream)", -5, srev1alpha1.PhaseExhausted},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := computePhase(c.remaining); got != c.want {
				t.Errorf("computePhase(%v) = %q, want %q", c.remaining, got, c.want)
			}
		})
	}
}

func TestClamp(t *testing.T) {
	cases := []struct {
		name      string
		v, lo, hi float64
		want      float64
	}{
		{"within range", 50, 0, 100, 50},
		{"below low", -10, 0, 100, 0},
		{"above high", 150, 0, 100, 100},
		{"at bounds low", 0, 0, 100, 0},
		{"at bounds high", 100, 0, 100, 100},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := clamp(c.v, c.lo, c.hi); got != c.want {
				t.Errorf("clamp(%v,%v,%v) = %v, want %v", c.v, c.lo, c.hi, got, c.want)
			}
		})
	}
}
