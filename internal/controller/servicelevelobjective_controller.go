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
	"strconv"
	"time"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	srev1alpha1 "github.com/RomanMasson1505/SLO-Custom-Ressource/api/v1alpha1"
	"github.com/RomanMasson1505/SLO-Custom-Ressource/internal/promclient"
	"github.com/RomanMasson1505/SLO-Custom-Ressource/internal/rules"
)

// requeueInterval is how often we re-evaluate a healthy SLO's budget.
const requeueInterval = 60 * time.Second

// prometheusBackoff is the slower retry used when Prometheus is unreachable, so
// we don't hammer the API while it is down.
const prometheusBackoff = 2 * time.Minute

// budgetExhaustedLabel is stamped on targeted Deployments once the budget is gone,
// so an external policy (e.g. a ValidatingAdmissionPolicy) can freeze rollouts.
const budgetExhaustedLabel = "slo.io/budget-exhausted"

// ServiceLevelObjectiveReconciler reconciles a ServiceLevelObjective object
type ServiceLevelObjectiveReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Prom     promclient.PromAPI
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=sre.slo.romanmasson.dev,resources=servicelevelobjectives,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sre.slo.romanmasson.dev,resources=servicelevelobjectives/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=sre.slo.romanmasson.dev,resources=servicelevelobjectives/finalizers,verbs=update
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=prometheusrules,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;update;patch

// Reconcile brings the cluster closer to the desired state described by one
// ServiceLevelObjective: it (re)generates the owned PrometheusRule, then queries
// Prometheus to evaluate the error budget and reports the result in the status.
func (r *ServiceLevelObjectiveReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// --- Layer 1: load the SLO ------------------------------------------------
	var slo srev1alpha1.ServiceLevelObjective
	if err := r.Get(ctx, req.NamespacedName, &slo); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Snapshot now, so every status patch below only sends the diff.
	base := client.MergeFrom(slo.DeepCopy())

	// --- Layer 3a: build the desired PrometheusRule (pure, may reject bad spec) -
	built, err := rules.Build(&slo)
	if err != nil {
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
	rule := &monitoringv1.PrometheusRule{
		ObjectMeta: metav1.ObjectMeta{Name: built.Name, Namespace: built.Namespace},
	}
	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, rule, func() error {
		rule.Labels = built.Labels
		rule.Spec = built.Spec
		return controllerutil.SetControllerReference(&slo, rule, r.Scheme)
	})
	if err != nil {
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
	meta.SetStatusCondition(&slo.Status.Conditions, metav1.Condition{
		Type:    srev1alpha1.CondRulesGenerated,
		Status:  metav1.ConditionTrue,
		Reason:  "Applied",
		Message: fmt.Sprintf("PrometheusRule %s %s", rule.Name, op),
	})

	// objective was already validated by rules.Build above; parse is safe here.
	objective, _ := strconv.ParseFloat(slo.Spec.Objective, 64)
	budget := 1 - objective/100 // 99.9 -> 0.001

	// --- Layer 4: query Prometheus -------------------------------------------
	// Error ratio over the full compliance window (drives the remaining budget)
	// and over 1h (drives the current burn rate).
	windowRatio, werr := r.queryRatio(ctx, &slo, slo.Spec.Window)
	hourRatio, herr := r.queryRatio(ctx, &slo, "1h")
	if qerr := firstErr(werr, herr); qerr != nil {
		log.Error(qerr, "prometheus query failed")
		meta.SetStatusCondition(&slo.Status.Conditions, metav1.Condition{
			Type:    srev1alpha1.CondPrometheusReachable,
			Status:  metav1.ConditionFalse,
			Reason:  "QueryFailed",
			Message: qerr.Error(),
		})
		slo.Status.Phase = srev1alpha1.PhaseUnknown
		slo.Status.ObservedGeneration = slo.Generation
		now := metav1.Now()
		slo.Status.LastEvaluationTime = &now
		if perr := r.Status().Patch(ctx, &slo, base); perr != nil {
			return ctrl.Result{}, perr
		}
		// nil error + backoff: retry later without a hot loop.
		return ctrl.Result{RequeueAfter: prometheusBackoff}, nil
	}
	meta.SetStatusCondition(&slo.Status.Conditions, metav1.Condition{
		Type:    srev1alpha1.CondPrometheusReachable,
		Status:  metav1.ConditionTrue,
		Reason:  "Queried",
		Message: "prometheus queried successfully",
	})

	// --- Layer 5: compute budget + phase (pure) ------------------------------
	remaining := clamp(100*(1-windowRatio/budget), 0, 100)
	burnRate := hourRatio / budget
	newPhase := computePhase(remaining)

	// --- Layer 7a: event on a real phase transition (before we overwrite it) --
	if newPhase != slo.Status.Phase && r.Recorder != nil {
		eventType := corev1.EventTypeNormal
		if newPhase != srev1alpha1.PhaseHealthy {
			eventType = corev1.EventTypeWarning
		}
		r.Recorder.Event(&slo, eventType, "PhaseChanged",
			fmt.Sprintf("phase %q -> %q (budget remaining %.2f%%)", slo.Status.Phase, newPhase, remaining))
	}

	// --- Layer 6: write the status -------------------------------------------
	slo.Status.ErrorBudgetRemaining = fmt.Sprintf("%.2f", remaining)
	slo.Status.CurrentBurnRate = fmt.Sprintf("%.2f", burnRate)
	slo.Status.Phase = newPhase
	slo.Status.ObservedGeneration = slo.Generation
	now := metav1.Now()
	slo.Status.LastEvaluationTime = &now
	meta.SetStatusCondition(&slo.Status.Conditions, metav1.Condition{
		Type:    srev1alpha1.CondBudgetHealthy,
		Status:  boolToCond(newPhase != srev1alpha1.PhaseExhausted),
		Reason:  "Evaluated",
		Message: fmt.Sprintf("%.2f%% budget remaining", remaining),
	})
	meta.SetStatusCondition(&slo.Status.Conditions, metav1.Condition{
		Type:    srev1alpha1.CondReady,
		Status:  metav1.ConditionTrue,
		Reason:  "Reconciled",
		Message: "SLO reconciled",
	})
	if err := r.Status().Patch(ctx, &slo, base); err != nil {
		return ctrl.Result{}, err
	}

	// --- Layer 8: enforcement (optional) -------------------------------------
	// Freeze/unfreeze the targeted Deployments based on the new phase.
	if err := r.reconcileEnforcement(ctx, &slo, newPhase); err != nil {
		log.Error(err, "enforcement failed")
		return ctrl.Result{}, err
	}

	// --- Layer 7b: come back to re-evaluate the budget periodically ----------
	return ctrl.Result{RequeueAfter: requeueInterval}, nil
}

// reconcileEnforcement labels (or unlabels) the Deployments selected by the SLO,
// depending on whether the budget is exhausted. It is a no-op unless the SLO
// opted in via spec.enforcement.freezeDeployments.
func (r *ServiceLevelObjectiveReconciler) reconcileEnforcement(
	ctx context.Context, slo *srev1alpha1.ServiceLevelObjective, phase string,
) error {
	e := slo.Spec.Enforcement
	if e == nil || !e.FreezeDeployments || e.Selector == nil {
		return nil // enforcement disabled (selector is guaranteed by the webhook)
	}

	selector, err := metav1.LabelSelectorAsSelector(e.Selector)
	if err != nil {
		return fmt.Errorf("invalid enforcement selector: %w", err)
	}

	var deployments appsv1.DeploymentList
	if err := r.List(ctx, &deployments,
		client.InNamespace(slo.Namespace),
		client.MatchingLabelsSelector{Selector: selector},
	); err != nil {
		return err
	}

	freeze := phase == srev1alpha1.PhaseExhausted
	for i := range deployments.Items {
		d := &deployments.Items[i]
		_, labeled := d.Labels[budgetExhaustedLabel]
		if freeze == labeled {
			continue // already in the desired state: nothing to do
		}

		patch := client.MergeFrom(d.DeepCopy())
		if freeze {
			if d.Labels == nil {
				d.Labels = map[string]string{}
			}
			d.Labels[budgetExhaustedLabel] = "true"
		} else {
			delete(d.Labels, budgetExhaustedLabel)
		}
		if err := r.Patch(ctx, d, patch); err != nil {
			return err
		}

		if r.Recorder != nil {
			verb := "Unfroze"
			if freeze {
				verb = "Froze"
			}
			r.Recorder.Event(slo, corev1.EventTypeWarning, "DeploymentFreeze",
				fmt.Sprintf("%s deployment %s/%s (phase %s)", verb, d.Namespace, d.Name, phase))
		}
	}
	return nil
}

// queryRatio computes error/total over the given window by substituting the
// window into the SLI queries and asking Prometheus for the ratio.
func (r *ServiceLevelObjectiveReconciler) queryRatio(
	ctx context.Context, slo *srev1alpha1.ServiceLevelObjective, window string,
) (float64, error) {
	errExpr, err := rules.SubstituteWindow(slo.Spec.SLI.ErrorQuery, window)
	if err != nil {
		return 0, err
	}
	totExpr, err := rules.SubstituteWindow(slo.Spec.SLI.TotalQuery, window)
	if err != nil {
		return 0, err
	}
	return r.Prom.QueryScalar(ctx, fmt.Sprintf("(%s) / (%s)", errExpr, totExpr))
}

// firstErr returns the first non-nil error, or nil.
func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// boolToCond converts a boolean to a Kubernetes ConditionStatus.
func boolToCond(b bool) metav1.ConditionStatus {
	if b {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}

// SetupWithManager sets up the controller with the Manager.
func (r *ServiceLevelObjectiveReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&srev1alpha1.ServiceLevelObjective{}).
		Owns(&monitoringv1.PrometheusRule{}). // re-reconcile if our child rule drifts
		Named("servicelevelobjective").
		Complete(r)
}
