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

package rules

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	slov1alpha1 "github.com/RomanMasson1505/SLO-Custom-Ressource/api/v1alpha1"
)

// Run `go test ./internal/rules -update` to (re)write the golden file after an
// intentional change to the generation logic.
var update = flag.Bool("update", false, "update golden files")

// sampleSLO is the fixed input used by the golden test. Keeping it here (rather
// than reading YAML) makes the test self-contained.
func sampleSLO() *slov1alpha1.ServiceLevelObjective {
	return &slov1alpha1.ServiceLevelObjective{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "shop"},
		Spec: slov1alpha1.ServiceLevelObjectiveSpec{
			Objective: "99.9",
			Window:    "30d",
			SLI: slov1alpha1.SLISpec{
				Type:       "availability",
				TotalQuery: `sum(rate(http_requests_total{service="checkout"}[{{.Window}}]))`,
				ErrorQuery: `sum(rate(http_requests_total{service="checkout",code=~"5.."}[{{.Window}}]))`,
			},
		},
	}
}

func TestSubstituteWindow(t *testing.T) {
	got, err := SubstituteWindow(`rate(x[{{.Window}}])`, "5m")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := `rate(x[5m])`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSubstituteWindow_BadTemplate(t *testing.T) {
	if _, err := SubstituteWindow(`rate(x[{{.Window)`, "5m"); err == nil {
		t.Fatal("expected an error for a malformed template, got nil")
	}
}

func TestBuild_RuleCounts(t *testing.T) {
	pr, err := Build(sampleSLO())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(pr.Spec.Groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(pr.Spec.Groups))
	}
	// 7 recording windows + 4 burn-rate alerts.
	if got, want := len(pr.Spec.Groups[0].Rules), len(recordingWindows)+len(burnLevels); got != want {
		t.Errorf("expected %d rules, got %d", want, got)
	}
}

func TestBuild_InvalidObjective(t *testing.T) {
	slo := sampleSLO()
	slo.Spec.Objective = "not-a-number"
	if _, err := Build(slo); err == nil {
		t.Fatal("expected an error for a non-numeric objective, got nil")
	}
}

func TestBuild_Golden(t *testing.T) {
	pr, err := Build(sampleSLO())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	got, err := yaml.Marshal(pr)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	goldenPath := filepath.Join("testdata", "checkout.golden.yaml")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", goldenPath)
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("reading golden (run with -update to create it): %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("generated PrometheusRule differs from golden.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}
