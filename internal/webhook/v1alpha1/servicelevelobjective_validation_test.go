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
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	srev1alpha1 "github.com/RomanMasson1505/SLO-Custom-Ressource/api/v1alpha1"
)

// validSLO returns a SLO that passes every check; tests mutate one field to
// exercise a single failure at a time.
func validSLO() *srev1alpha1.ServiceLevelObjective {
	return &srev1alpha1.ServiceLevelObjective{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "shop"},
		Spec: srev1alpha1.ServiceLevelObjectiveSpec{
			Objective: "99.9",
			Window:    "30d",
			SLI: srev1alpha1.SLISpec{
				Type:       "availability",
				TotalQuery: `sum(rate(http_requests_total{service="checkout"}[{{.Window}}]))`,
				ErrorQuery: `sum(rate(http_requests_total{service="checkout",code=~"5.."}[{{.Window}}]))`,
			},
		},
	}
}

func TestDefault_FillsWindow(t *testing.T) {
	d := &ServiceLevelObjectiveCustomDefaulter{}

	slo := validSLO()
	slo.Spec.Window = ""
	if err := d.Default(context.Background(), slo); err != nil {
		t.Fatalf("Default: %v", err)
	}
	if slo.Spec.Window != defaultWindow {
		t.Errorf("expected window defaulted to %q, got %q", defaultWindow, slo.Spec.Window)
	}

	// A window already set must be left untouched.
	slo2 := validSLO()
	slo2.Spec.Window = "7d"
	_ = d.Default(context.Background(), slo2)
	if slo2.Spec.Window != "7d" {
		t.Errorf("expected window preserved as 7d, got %q", slo2.Spec.Window)
	}
}

func TestValidateCreate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*srev1alpha1.ServiceLevelObjective)
		wantErr bool
	}{
		{"valid", func(_ *srev1alpha1.ServiceLevelObjective) {}, false},
		{"objective not a number", func(s *srev1alpha1.ServiceLevelObjective) { s.Spec.Objective = "abc" }, true},
		{"objective too high", func(s *srev1alpha1.ServiceLevelObjective) { s.Spec.Objective = "150" }, true},
		{"objective zero", func(s *srev1alpha1.ServiceLevelObjective) { s.Spec.Objective = "0" }, true},
		{"objective 100", func(s *srev1alpha1.ServiceLevelObjective) { s.Spec.Objective = "100" }, true},
		{"bad window", func(s *srev1alpha1.ServiceLevelObjective) { s.Spec.Window = "banana" }, true},
		{"bad total query", func(s *srev1alpha1.ServiceLevelObjective) { s.Spec.SLI.TotalQuery = "sum(rate(" }, true},
		{"bad error query", func(s *srev1alpha1.ServiceLevelObjective) { s.Spec.SLI.ErrorQuery = "))(" }, true},
		{"freeze without selector", func(s *srev1alpha1.ServiceLevelObjective) {
			s.Spec.Enforcement = &srev1alpha1.EnforcementSpec{FreezeDeployments: true}
		}, true},
		{"freeze with selector", func(s *srev1alpha1.ServiceLevelObjective) {
			s.Spec.Enforcement = &srev1alpha1.EnforcementSpec{
				FreezeDeployments: true,
				Selector:          &metav1.LabelSelector{MatchLabels: map[string]string{"app": "checkout"}},
			}
		}, false},
	}

	v := &ServiceLevelObjectiveCustomValidator{}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			slo := validSLO()
			c.mutate(slo)
			_, err := v.ValidateCreate(context.Background(), slo)
			if c.wantErr && err == nil {
				t.Errorf("expected an error, got nil")
			}
			if !c.wantErr && err != nil {
				t.Errorf("expected no error, got %v", err)
			}
			// Rejections must be reported as a Kubernetes "Invalid" error.
			if c.wantErr && err != nil && !apierrors.IsInvalid(err) {
				t.Errorf("expected an Invalid API error, got %T: %v", err, err)
			}
		})
	}
}

func TestValidateUpdate_TypeImmutable(t *testing.T) {
	v := &ServiceLevelObjectiveCustomValidator{}

	oldObj := validSLO() // type "availability"
	newObj := validSLO()
	newObj.Spec.SLI.Type = "latency"

	if _, err := v.ValidateUpdate(context.Background(), oldObj, newObj); err == nil {
		t.Fatal("expected an error when changing sli.type, got nil")
	}

	// Same type, valid change elsewhere -> allowed.
	newObj2 := validSLO()
	newObj2.Spec.Objective = "99.5"
	if _, err := v.ValidateUpdate(context.Background(), oldObj, newObj2); err != nil {
		t.Errorf("expected no error for a valid update, got %v", err)
	}
}
