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

// Package rules turns a ServiceLevelObjective into a PrometheusRule.
//
// It is deliberately "pure": every function here takes data in and returns
// data out, without touching the apiserver, Prometheus, or the disk. That is
// what lets us test it with plain `go test` and a golden file, no cluster
// required.
package rules

import (
	"bytes"
	"fmt"
	"strconv"
	"text/template"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	slov1alpha1 "github.com/RomanMasson1505/SLO-Custom-Ressource/api/v1alpha1"
)

// metricPrefix is the base name of the recorded error-ratio metric. The window
// (e.g. "5m") is appended, giving "slo:sli_error:ratio_rate5m". The colons are
// the Prometheus convention for "this is a recording rule, not a raw metric".
const metricPrefix = "slo:sli_error:ratio_rate"

// Labels stamped on every generated rule so alerts can be traced back to their SLO.
const (
	labelSLOName      = "slo_name"
	labelSLONamespace = "slo_namespace"
)

// recordingWindows is every rolling window we pre-compute the error ratio for.
// The alerting rules below only ever reference windows from this list.
var recordingWindows = []string{"5m", "30m", "1h", "2h", "6h", "1d", "3d"}

// burnLevel is one row of the multi-window multi-burn-rate table from the
// Google SRE Workbook. Each alert compares a short and a long window against
// the same threshold, so a real problem fires fast (short) but transient blips
// are filtered out (long).
type burnLevel struct {
	severity string  // routed to page vs ticket by Alertmanager
	short    string  // fast window, resets the alert quickly once fixed
	long     string  // slow window, confirms the problem is significant
	factor   float64 // how many times faster than "spend it exactly over the window" we burn
}

// burnLevels: the four canonical alerts. factor 14.4 burns a 30d budget in ~2d.
var burnLevels = []burnLevel{
	{severity: "critical", short: "5m", long: "1h", factor: 14.4},
	{severity: "critical", short: "30m", long: "6h", factor: 6},
	{severity: "warning", short: "2h", long: "1d", factor: 3},
	{severity: "warning", short: "6h", long: "3d", factor: 1},
}

// SubstituteWindow replaces the {{.Window}} placeholder in a PromQL query with a
// concrete duration. The controller also calls this (for the status query), so
// it is exported.
func SubstituteWindow(query, window string) (string, error) {
	tmpl, err := template.New("promql").Parse(query)
	if err != nil {
		return "", fmt.Errorf("parsing query template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, map[string]string{"Window": window}); err != nil {
		return "", fmt.Errorf("rendering query template: %w", err)
	}
	return buf.String(), nil
}

// buildRecordingRules pre-computes error/total for every window. Alerts then read
// these cheap, stored series instead of re-running the raw PromQL each evaluation.
func buildRecordingRules(slo *slov1alpha1.ServiceLevelObjective) ([]monitoringv1.Rule, error) {
	out := make([]monitoringv1.Rule, 0, len(recordingWindows))
	for _, w := range recordingWindows {
		errExpr, err := SubstituteWindow(slo.Spec.SLI.ErrorQuery, w)
		if err != nil {
			return nil, fmt.Errorf("error query, window %s: %w", w, err)
		}
		totExpr, err := SubstituteWindow(slo.Spec.SLI.TotalQuery, w)
		if err != nil {
			return nil, fmt.Errorf("total query, window %s: %w", w, err)
		}
		out = append(out, monitoringv1.Rule{
			Record: metricPrefix + w,
			Expr:   intstr.FromString(fmt.Sprintf("(%s) / (%s)", errExpr, totExpr)),
			Labels: map[string]string{
				labelSLOName:      slo.Name,
				labelSLONamespace: slo.Namespace,
			},
		})
	}
	return out, nil
}

// buildAlertingRules turns each burnLevel into one alert that fires only when
// BOTH its short and long windows exceed factor*budget.
func buildAlertingRules(slo *slov1alpha1.ServiceLevelObjective, budget float64) []monitoringv1.Rule {
	out := make([]monitoringv1.Rule, 0, len(burnLevels))
	for _, l := range burnLevels {
		// Round to 6 significant digits so the emitted PromQL reads "0.0144",
		// not the float64 noise "0.014399999999998414".
		threshold := strconv.FormatFloat(l.factor*budget, 'g', 6, 64)
		expr := fmt.Sprintf("%s%s{slo_name=%q} > %s and %s%s{slo_name=%q} > %s",
			metricPrefix, l.short, slo.Name, threshold,
			metricPrefix, l.long, slo.Name, threshold,
		)
		out = append(out, monitoringv1.Rule{
			Alert: "SLOErrorBudgetBurn",
			Expr:  intstr.FromString(expr),
			For:   ptr.To(monitoringv1.Duration("2m")),
			Labels: map[string]string{
				"severity":        l.severity,
				labelSLOName:      slo.Name,
				labelSLONamespace: slo.Namespace,
			},
			Annotations: map[string]string{
				"summary": fmt.Sprintf("SLO %s is burning error budget %gx too fast", slo.Name, l.factor),
			},
		})
	}
	return out
}

// Build assembles the full PrometheusRule from an SLO. Recording rules come
// first, then the alerts, all in a single rule group.
func Build(slo *slov1alpha1.ServiceLevelObjective) (*monitoringv1.PrometheusRule, error) {
	objective, err := strconv.ParseFloat(slo.Spec.Objective, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid objective %q: %w", slo.Spec.Objective, err)
	}
	// budget is the fraction of requests we are allowed to fail: 99.9 -> 0.001.
	budget := 1 - objective/100

	recording, err := buildRecordingRules(slo)
	if err != nil {
		return nil, err
	}
	alerts := buildAlertingRules(slo, budget)

	return &monitoringv1.PrometheusRule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      slo.Name + "-slo-rules",
			Namespace: slo.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "slo-operator",
				labelSLOName:                   slo.Name,
			},
		},
		Spec: monitoringv1.PrometheusRuleSpec{
			Groups: []monitoringv1.RuleGroup{{
				Name:  "slo.rules",
				Rules: append(recording, alerts...),
			}},
		},
	}, nil
}
