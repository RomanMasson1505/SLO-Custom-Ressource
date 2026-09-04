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
	"errors"
	"testing"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	srev1alpha1 "github.com/RomanMasson1505/SLO-Custom-Ressource/api/v1alpha1"
)

// fakeProm is an in-memory PromAPI: every query returns the same ratio (or err).
type fakeProm struct {
	ratio float64
	err   error
}

func (f fakeProm) QueryScalar(_ context.Context, _ string) (float64, error) {
	return f.ratio, f.err
}

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
	if err := appsv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func testSLO() *srev1alpha1.ServiceLevelObjective {
	return &srev1alpha1.ServiceLevelObjective{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "shop"},
		Spec: srev1alpha1.ServiceLevelObjectiveSpec{
			Objective: "99.9", // budget = 0.001
			Window:    "30d",
			SLI: srev1alpha1.SLISpec{
				Type:       "availability",
				TotalQuery: `sum(rate(http_requests_total{service="checkout"}[{{.Window}}]))`,
				ErrorQuery: `sum(rate(http_requests_total{service="checkout",code=~"5.."}[{{.Window}}]))`,
			},
		},
	}
}

// newFixture wires a fake client + a reconciler with an injected fake Prometheus.
func newFixture(t *testing.T, slo *srev1alpha1.ServiceLevelObjective, prom fakeProm) (*ServiceLevelObjectiveReconciler, client.Client) {
	t.Helper()
	scheme := newScheme(t)
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(slo).
		WithStatusSubresource(slo).
		Build()
	r := &ServiceLevelObjectiveReconciler{
		Client:   cl,
		Scheme:   scheme,
		Prom:     prom,
		Recorder: record.NewFakeRecorder(10),
	}
	return r, cl
}

var checkoutKey = types.NamespacedName{Name: "checkout", Namespace: "shop"}

func TestReconcile_CreatesOwnedPrometheusRule(t *testing.T) {
	r, cl := newFixture(t, testSLO(), fakeProm{ratio: 0.0005})
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: checkoutKey}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var pr monitoringv1.PrometheusRule
	if err := cl.Get(context.Background(),
		types.NamespacedName{Name: "checkout-slo-rules", Namespace: "shop"}, &pr); err != nil {
		t.Fatalf("expected PrometheusRule to exist: %v", err)
	}
	if len(pr.OwnerReferences) != 1 {
		t.Fatalf("expected 1 owner reference, got %d", len(pr.OwnerReferences))
	}
	if owner := pr.OwnerReferences[0]; owner.Kind != "ServiceLevelObjective" || owner.Name != "checkout" {
		t.Errorf("unexpected owner: %+v", owner)
	}
	if len(pr.Spec.Groups) != 1 || len(pr.Spec.Groups[0].Rules) != 11 {
		t.Errorf("expected 1 group of 11 rules")
	}

	var got srev1alpha1.ServiceLevelObjective
	if err := cl.Get(context.Background(), checkoutKey, &got); err != nil {
		t.Fatal(err)
	}
	if c := meta.FindStatusCondition(got.Status.Conditions, srev1alpha1.CondRulesGenerated); c == nil || c.Status != metav1.ConditionTrue {
		t.Errorf("expected RulesGenerated=True, got %+v", c)
	}
}

func TestReconcile_Idempotent(t *testing.T) {
	r, cl := newFixture(t, testSLO(), fakeProm{ratio: 0.0005})
	for i := range 2 {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: checkoutKey}); err != nil {
			t.Fatalf("Reconcile pass %d: %v", i, err)
		}
	}
	var list monitoringv1.PrometheusRuleList
	if err := cl.List(context.Background(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 {
		t.Errorf("expected exactly 1 PrometheusRule after two reconciles, got %d", len(list.Items))
	}
}

func TestReconcile_HealthyBudget(t *testing.T) {
	// ratio 0.0005 with budget 0.001 => 50%% consumed => 50%% remaining.
	r, cl := newFixture(t, testSLO(), fakeProm{ratio: 0.0005})
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: checkoutKey})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.RequeueAfter != requeueInterval {
		t.Errorf("expected requeue after %v, got %v", requeueInterval, res.RequeueAfter)
	}

	var got srev1alpha1.ServiceLevelObjective
	if err := cl.Get(context.Background(), checkoutKey, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != srev1alpha1.PhaseHealthy {
		t.Errorf("expected phase Healthy, got %q", got.Status.Phase)
	}
	if got.Status.ErrorBudgetRemaining != "50.00" {
		t.Errorf("expected 50.00%% remaining, got %q", got.Status.ErrorBudgetRemaining)
	}
	if got.Status.CurrentBurnRate != "0.50" {
		t.Errorf("expected burn rate 0.50, got %q", got.Status.CurrentBurnRate)
	}
}

func TestReconcile_ExhaustedBudget(t *testing.T) {
	// ratio 0.002 with budget 0.001 => 200%% consumed => clamped to 0%% remaining.
	r, cl := newFixture(t, testSLO(), fakeProm{ratio: 0.002})
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: checkoutKey}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var got srev1alpha1.ServiceLevelObjective
	if err := cl.Get(context.Background(), checkoutKey, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != srev1alpha1.PhaseExhausted {
		t.Errorf("expected phase Exhausted, got %q", got.Status.Phase)
	}
	if got.Status.ErrorBudgetRemaining != "0.00" {
		t.Errorf("expected 0.00%% remaining, got %q", got.Status.ErrorBudgetRemaining)
	}
	if c := meta.FindStatusCondition(got.Status.Conditions, srev1alpha1.CondBudgetHealthy); c == nil || c.Status != metav1.ConditionFalse {
		t.Errorf("expected BudgetHealthy=False, got %+v", c)
	}
}

func TestReconcile_PrometheusUnreachable(t *testing.T) {
	r, cl := newFixture(t, testSLO(), fakeProm{err: errors.New("connection refused")})
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: checkoutKey})
	if err != nil {
		t.Fatalf("unreachable Prometheus should not return an error (no hot loop), got: %v", err)
	}
	if res.RequeueAfter != prometheusBackoff {
		t.Errorf("expected backoff requeue %v, got %v", prometheusBackoff, res.RequeueAfter)
	}

	var got srev1alpha1.ServiceLevelObjective
	if err := cl.Get(context.Background(), checkoutKey, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != srev1alpha1.PhaseUnknown {
		t.Errorf("expected phase Unknown, got %q", got.Status.Phase)
	}
	if c := meta.FindStatusCondition(got.Status.Conditions, srev1alpha1.CondPrometheusReachable); c == nil || c.Status != metav1.ConditionFalse {
		t.Errorf("expected PrometheusReachable=False, got %+v", c)
	}
}

func TestReconcile_InvalidObjectiveDoesNotRequeue(t *testing.T) {
	slo := testSLO()
	slo.Spec.Objective = "not-a-number"
	r, cl := newFixture(t, slo, fakeProm{}) // Prom never called: Build fails first

	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: checkoutKey})
	if err != nil {
		t.Fatalf("invalid spec should not return an error, got: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("invalid spec should not requeue, got %+v", res)
	}

	var list monitoringv1.PrometheusRuleList
	if err := cl.List(context.Background(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Errorf("expected no PrometheusRule for an invalid SLO, got %d", len(list.Items))
	}

	var got srev1alpha1.ServiceLevelObjective
	if err := cl.Get(context.Background(), checkoutKey, &got); err != nil {
		t.Fatal(err)
	}
	if c := meta.FindStatusCondition(got.Status.Conditions, srev1alpha1.CondRulesGenerated); c == nil || c.Status != metav1.ConditionFalse {
		t.Errorf("expected RulesGenerated=False, got %+v", c)
	}
}
