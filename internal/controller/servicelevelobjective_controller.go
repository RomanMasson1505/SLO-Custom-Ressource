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
	"fmt"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	srev1alpha1 "github.com/RomanMasson1505/SLO-Custom-Ressource/api/v1alpha1"
	"github.com/RomanMasson1505/SLO-Custom-Ressource/internal/rules"
)

// ServiceLevelObjectiveReconciler reconciles a ServiceLevelObjective object
type ServiceLevelObjectiveReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=sre.slo.romanmasson.dev,resources=servicelevelobjectives,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sre.slo.romanmasson.dev,resources=servicelevelobjectives/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=sre.slo.romanmasson.dev,resources=servicelevelobjectives/finalizers,verbs=update
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=prometheusrules,verbs=get;list;watch;create;update;patch;delete

// Reconcile brings the cluster closer to the desired state described by one
// ServiceLevelObjective: it (re)generates the PrometheusRule that implements the
// SLO's burn-rate alerting, owned by the SLO so it is garbage-collected with it.
func (r *ServiceLevelObjectiveReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// --- Layer 1: load the SLO ------------------------------------------------
	// Reconcile only receives a name; we fetch the actual object. If it was
	// deleted between the event and now, Get returns NotFound: nothing to do.
	var slo srev1alpha1.ServiceLevelObjective
	if err := r.Get(ctx, req.NamespacedName, &slo); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Snapshot the object now, so the status patch below only sends the diff.
	base := client.MergeFrom(slo.DeepCopy())

	// --- Layer 3a: build the desired PrometheusRule (pure, may reject bad spec) -
	built, err := rules.Build(&slo)
	if err != nil {
		// Invalid user input (e.g. objective is not a number). A webhook will
		// catch this earlier once implemented; here we just record it and stop
		// without requeueing, to avoid a hot loop on a spec we cannot fix.
		meta.SetStatusCondition(&slo.Status.Conditions, metav1.Condition{
			Type:    srev1alpha1.CondRulesGenerated,
			Status:  metav1.ConditionFalse,
			Reason:  "InvalidSpec",
			Message: err.Error(),
		})
		slo.Status.ObservedGeneration = slo.Generation
		if perr := r.Status().Patch(ctx, &slo, base); perr != nil {
			return ctrl.Result{}, perr
		}
		log.Error(err, "invalid SLO spec, not requeueing")
		return ctrl.Result{}, nil
	}

	// --- Layer 3b: create or update the PrometheusRule, idempotently ----------
	// We address the child by name; CreateOrUpdate creates it if missing, or
	// mutates the existing one to match `built`. The mutate func also stamps the
	// OwnerReference so deleting the SLO cascades to this rule.
	rule := &monitoringv1.PrometheusRule{
		ObjectMeta: metav1.ObjectMeta{Name: built.Name, Namespace: built.Namespace},
	}
	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, rule, func() error {
		rule.Labels = built.Labels
		rule.Spec = built.Spec
		return controllerutil.SetControllerReference(&slo, rule, r.Scheme)
	})
	if err != nil {
		// A real cluster/API error: record it and requeue (return the error).
		meta.SetStatusCondition(&slo.Status.Conditions, metav1.Condition{
			Type:    srev1alpha1.CondRulesGenerated,
			Status:  metav1.ConditionFalse,
			Reason:  "ApplyFailed",
			Message: err.Error(),
		})
		_ = r.Status().Patch(ctx, &slo, base)
		return ctrl.Result{}, err
	}
	log.Info("reconciled PrometheusRule", "operation", op, "name", rule.Name)

	// --- Layer 6 (minimal for now): write the status --------------------------
	// Budget/phase come in Phase 2 (querying Prometheus). For now we only assert
	// that the rules were generated and record the reconciled generation.
	meta.SetStatusCondition(&slo.Status.Conditions, metav1.Condition{
		Type:    srev1alpha1.CondRulesGenerated,
		Status:  metav1.ConditionTrue,
		Reason:  "Applied",
		Message: fmt.Sprintf("PrometheusRule %s %s", rule.Name, op),
	})
	slo.Status.ObservedGeneration = slo.Generation
	if err := r.Status().Patch(ctx, &slo, base); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ServiceLevelObjectiveReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&srev1alpha1.ServiceLevelObjective{}).
		Owns(&monitoringv1.PrometheusRule{}). // re-reconcile if our child rule drifts
		Named("servicelevelobjective").
		Complete(r)
}
