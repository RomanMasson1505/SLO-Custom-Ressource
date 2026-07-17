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
	"context"
	"testing"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	srev1alpha1 "github.com/RomanMasson1505/SLO-Custom-Ressource/api/v1alpha1"
)

// newScheme builds a scheme that knows both our SLO type and PrometheusRule,
// which the fake client needs to (de)serialize them.
func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := srev1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := monitoringv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func testSLO() *srev1alpha1.ServiceLevelObjective {
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

func reconcileOnce(t *testing.T, cl client.Client, scheme *runtime.Scheme, key types.NamespacedName) {
	t.Helper()
	r := &ServiceLevelObjectiveReconciler{Client: cl, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("Reconcile returned an error: %v", err)
	}
}

func TestReconcile_CreatesOwnedPrometheusRule(t *testing.T) {
	scheme := newScheme(t)
	slo := testSLO()
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(slo).
		WithStatusSubresource(slo). // required so Status().Patch works on the fake
		Build()

	key := types.NamespacedName{Name: "checkout", Namespace: "shop"}
	reconcileOnce(t, cl, scheme, key)

	// The PrometheusRule must now exist, named "<slo>-slo-rules".
	var pr monitoringv1.PrometheusRule
	if err := cl.Get(context.Background(),
		types.NamespacedName{Name: "checkout-slo-rules", Namespace: "shop"}, &pr); err != nil {
		t.Fatalf("expected PrometheusRule to exist: %v", err)
	}

	// It must carry an OwnerReference back to the SLO (cascade deletion).
	if len(pr.OwnerReferences) != 1 {
		t.Fatalf("expected 1 owner reference, got %d", len(pr.OwnerReferences))
	}
	if owner := pr.OwnerReferences[0]; owner.Kind != "ServiceLevelObjective" || owner.Name != "checkout" {
		t.Errorf("unexpected owner: %+v", owner)
	}

	// And it must contain the 11 generated rules (7 recording + 4 alerts).
	if len(pr.Spec.Groups) != 1 || len(pr.Spec.Groups[0].Rules) != 11 {
		t.Errorf("expected 1 group of 11 rules, got groups=%d", len(pr.Spec.Groups))
	}

	// The SLO status must report RulesGenerated=True.
	var got srev1alpha1.ServiceLevelObjective
	if err := cl.Get(context.Background(), key, &got); err != nil {
		t.Fatal(err)
	}
	if c := meta.FindStatusCondition(got.Status.Conditions, srev1alpha1.CondRulesGenerated); c == nil {
		t.Fatal("expected RulesGenerated condition to be set")
	} else if c.Status != metav1.ConditionTrue {
		t.Errorf("expected RulesGenerated=True, got %s", c.Status)
	}
}

func TestReconcile_Idempotent(t *testing.T) {
	scheme := newScheme(t)
	slo := testSLO()
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(slo).
		WithStatusSubresource(slo).
		Build()

	key := types.NamespacedName{Name: "checkout", Namespace: "shop"}
	reconcileOnce(t, cl, scheme, key)
	reconcileOnce(t, cl, scheme, key) // second pass must not error or duplicate

	var list monitoringv1.PrometheusRuleList
	if err := cl.List(context.Background(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 {
		t.Errorf("expected exactly 1 PrometheusRule after two reconciles, got %d", len(list.Items))
	}
}

func TestReconcile_InvalidObjectiveDoesNotRequeue(t *testing.T) {
	scheme := newScheme(t)
	slo := testSLO()
	slo.Spec.Objective = "not-a-number"
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(slo).
		WithStatusSubresource(slo).
		Build()

	key := types.NamespacedName{Name: "checkout", Namespace: "shop"}
	r := &ServiceLevelObjectiveReconciler{Client: cl, Scheme: scheme}
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	if err != nil {
		t.Fatalf("invalid spec should not return an error (no hot loop), got: %v", err)
	}
	if res.Requeue || res.RequeueAfter != 0 {
		t.Errorf("invalid spec should not requeue, got %+v", res)
	}

	// No PrometheusRule should have been created.
	var list monitoringv1.PrometheusRuleList
	if err := cl.List(context.Background(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Errorf("expected no PrometheusRule for an invalid SLO, got %d", len(list.Items))
	}

	// Status should report RulesGenerated=False.
	var got srev1alpha1.ServiceLevelObjective
	if err := cl.Get(context.Background(), key, &got); err != nil {
		t.Fatal(err)
	}
	if c := meta.FindStatusCondition(got.Status.Conditions, srev1alpha1.CondRulesGenerated); c == nil || c.Status != metav1.ConditionFalse {
		t.Errorf("expected RulesGenerated=False, got %+v", c)
	}
}
