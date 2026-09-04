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
	"fmt"
	"strconv"

	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/promql/parser"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	srev1alpha1 "github.com/RomanMasson1505/SLO-Custom-Ressource/api/v1alpha1"
	"github.com/RomanMasson1505/SLO-Custom-Ressource/internal/rules"
)

// defaultWindow is applied when the user leaves spec.window empty.
const defaultWindow = "30d"

// promqlParser is a stateless PromQL parser reused for every validation.
var promqlParser = parser.NewParser(parser.Options{})

// nolint:unused
// log is for logging in this package.
var servicelevelobjectivelog = logf.Log.WithName("servicelevelobjective-resource")

// SetupServiceLevelObjectiveWebhookWithManager registers the webhook for ServiceLevelObjective in the manager.
func SetupServiceLevelObjectiveWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &srev1alpha1.ServiceLevelObjective{}).
		WithValidator(&ServiceLevelObjectiveCustomValidator{}).
		WithDefaulter(&ServiceLevelObjectiveCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-sre-slo-romanmasson-dev-v1alpha1-servicelevelobjective,mutating=true,failurePolicy=fail,sideEffects=None,groups=sre.slo.romanmasson.dev,resources=servicelevelobjectives,verbs=create;update,versions=v1alpha1,name=mservicelevelobjective-v1alpha1.kb.io,admissionReviewVersions=v1

// ServiceLevelObjectiveCustomDefaulter fills in default values before the object is stored.
type ServiceLevelObjectiveCustomDefaulter struct{}

// Default implements webhook.CustomDefaulter.
func (d *ServiceLevelObjectiveCustomDefaulter) Default(_ context.Context, obj *srev1alpha1.ServiceLevelObjective) error {
	servicelevelobjectivelog.Info("Defaulting for ServiceLevelObjective", "name", obj.GetName())

	if obj.Spec.Window == "" {
		obj.Spec.Window = defaultWindow
	}
	return nil
}

// +kubebuilder:webhook:path=/validate-sre-slo-romanmasson-dev-v1alpha1-servicelevelobjective,mutating=false,failurePolicy=fail,sideEffects=None,groups=sre.slo.romanmasson.dev,resources=servicelevelobjectives,verbs=create;update,versions=v1alpha1,name=vservicelevelobjective-v1alpha1.kb.io,admissionReviewVersions=v1

// ServiceLevelObjectiveCustomValidator rejects invalid ServiceLevelObjective specs
// before they ever reach etcd.
type ServiceLevelObjectiveCustomValidator struct{}

// ValidateCreate validates a brand-new SLO.
func (v *ServiceLevelObjectiveCustomValidator) ValidateCreate(_ context.Context, obj *srev1alpha1.ServiceLevelObjective) (admission.Warnings, error) {
	servicelevelobjectivelog.Info("Validation upon creation", "name", obj.GetName())
	return nil, toInvalidError(obj, validateSpec(obj))
}

// ValidateUpdate validates a change, plus the immutability of spec.sli.type.
func (v *ServiceLevelObjectiveCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj *srev1alpha1.ServiceLevelObjective) (admission.Warnings, error) {
	servicelevelobjectivelog.Info("Validation upon update", "name", newObj.GetName())

	allErrs := validateSpec(newObj)
	if oldObj.Spec.SLI.Type != newObj.Spec.SLI.Type {
		allErrs = append(allErrs, field.Invalid(
			field.NewPath("spec", "sli", "type"),
			newObj.Spec.SLI.Type,
			fmt.Sprintf("field is immutable (was %q)", oldObj.Spec.SLI.Type),
		))
	}
	return nil, toInvalidError(newObj, allErrs)
}

// ValidateDelete has nothing to check.
func (v *ServiceLevelObjectiveCustomValidator) ValidateDelete(_ context.Context, _ *srev1alpha1.ServiceLevelObjective) (admission.Warnings, error) {
	return nil, nil
}

// validateSpec runs every field-level check and collects all problems at once,
// so the user sees every error in a single kubectl message instead of one at a time.
func validateSpec(slo *srev1alpha1.ServiceLevelObjective) field.ErrorList {
	var allErrs field.ErrorList
	spec := field.NewPath("spec")

	// objective must be a number strictly inside (0, 100).
	if obj, err := strconv.ParseFloat(slo.Spec.Objective, 64); err != nil {
		allErrs = append(allErrs, field.Invalid(spec.Child("objective"), slo.Spec.Objective,
			`must be a number, e.g. "99.9"`))
	} else if obj <= 0 || obj >= 100 {
		allErrs = append(allErrs, field.Invalid(spec.Child("objective"), slo.Spec.Objective,
			"must be in the open interval (0, 100)"))
	}

	// window must be a valid Prometheus duration.
	if _, err := model.ParseDuration(slo.Spec.Window); err != nil {
		allErrs = append(allErrs, field.Invalid(spec.Child("window"), slo.Spec.Window,
			`must be a valid Prometheus duration, e.g. "30d"`))
	}

	// both SLI queries must be valid PromQL (after substituting a dummy window).
	sli := spec.Child("sli")
	allErrs = append(allErrs, validatePromQL(sli.Child("totalQuery"), slo.Spec.SLI.TotalQuery)...)
	allErrs = append(allErrs, validatePromQL(sli.Child("errorQuery"), slo.Spec.SLI.ErrorQuery)...)

	// freezing deployments requires a selector to know which ones to freeze.
	if e := slo.Spec.Enforcement; e != nil && e.FreezeDeployments && e.Selector == nil {
		allErrs = append(allErrs, field.Required(spec.Child("enforcement", "selector"),
			"required when enforcement.freezeDeployments is true"))
	}

	return allErrs
}

// validatePromQL substitutes a placeholder window then parses the query with
// Prometheus' own parser, so anything we accept, Prometheus will accept too.
func validatePromQL(path *field.Path, query string) field.ErrorList {
	substituted, err := rules.SubstituteWindow(query, "5m")
	if err != nil {
		return field.ErrorList{field.Invalid(path, query,
			fmt.Sprintf("invalid {{.Window}} template: %v", err))}
	}
	if _, err := promqlParser.ParseExpr(substituted); err != nil {
		return field.ErrorList{field.Invalid(path, query,
			fmt.Sprintf("invalid PromQL: %v", err))}
	}
	return nil
}

// toInvalidError turns a list of field errors into the standard Kubernetes
// "Invalid" API error (nil if the list is empty).
func toInvalidError(slo *srev1alpha1.ServiceLevelObjective, allErrs field.ErrorList) error {
	if len(allErrs) == 0 {
		return nil
	}
	return apierrors.NewInvalid(
		schema.GroupKind{Group: srev1alpha1.GroupVersion.Group, Kind: "ServiceLevelObjective"},
		slo.Name,
		allErrs,
	)
}
