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

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	srev1alpha1 "github.com/RomanMasson1505/SLO-Custom-Ressource/api/v1alpha1"
)

// enforcingSLO is a SLO that freezes deployments matching app=checkout.
func enforcingSLO() *srev1alpha1.ServiceLevelObjective {
	slo := testSLO()
	slo.Spec.Enforcement = &srev1alpha1.EnforcementSpec{
		FreezeDeployments: true,
		Selector:          &metav1.LabelSelector{MatchLabels: map[string]string{"app": "checkout"}},
	}
	return slo
}

func checkoutDeployment(labels map[string]string) *appsv1.Deployment {
	l := map[string]string{"app": "checkout"}
	for k, v := range labels {
		l[k] = v
	}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "shop", Labels: l},
	}
}

// fixtureWithDeployment wires a reconciler whose fake client also holds a Deployment.
func fixtureWithDeployment(
	t *testing.T, slo *srev1alpha1.ServiceLevelObjective, dep *appsv1.Deployment, prom fakeProm,
) (*ServiceLevelObjectiveReconciler, client.Client) {
	t.Helper()
	scheme := newScheme(t)
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(slo, dep).
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

func deploymentLabeled(t *testing.T, cl client.Client) bool {
	t.Helper()
	var d appsv1.Deployment
	if err := cl.Get(context.Background(),
		types.NamespacedName{Name: "checkout", Namespace: "shop"}, &d); err != nil {
		t.Fatal(err)
	}
	_, ok := d.Labels[budgetExhaustedLabel]
	return ok
}

func TestEnforcement_FreezesOnExhausted(t *testing.T) {
	// ratio 0.002 with budget 0.001 => exhausted.
	r, cl := fixtureWithDeployment(t, enforcingSLO(), checkoutDeployment(nil), fakeProm{ratio: 0.002})
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: checkoutKey}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !deploymentLabeled(t, cl) {
		t.Error("expected deployment to be labeled budget-exhausted when phase is Exhausted")
	}
}

func TestEnforcement_UnfreezesWhenHealthy(t *testing.T) {
	// Deployment starts already frozen; a healthy budget must clear the label.
	dep := checkoutDeployment(map[string]string{budgetExhaustedLabel: "true"})
	r, cl := fixtureWithDeployment(t, enforcingSLO(), dep, fakeProm{ratio: 0.0005})
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: checkoutKey}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if deploymentLabeled(t, cl) {
		t.Error("expected deployment label to be removed when phase is Healthy")
	}
}

func TestEnforcement_DisabledLeavesDeploymentAlone(t *testing.T) {
	// SLO without enforcement + exhausted budget: deployment must stay untouched.
	r, cl := fixtureWithDeployment(t, testSLO(), checkoutDeployment(nil), fakeProm{ratio: 0.002})
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: checkoutKey}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if deploymentLabeled(t, cl) {
		t.Error("expected deployment to be untouched when enforcement is disabled")
	}
}

func TestEnforcement_IgnoresNonMatchingDeployment(t *testing.T) {
	// A deployment that doesn't match the selector must not be frozen.
	other := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "shop", Labels: map[string]string{"app": "billing"}},
	}
	r, cl := fixtureWithDeployment(t, enforcingSLO(), other, fakeProm{ratio: 0.002})
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: checkoutKey}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var d appsv1.Deployment
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "other", Namespace: "shop"}, &d); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.Labels[budgetExhaustedLabel]; ok {
		t.Error("expected non-matching deployment to be left alone")
	}
}
