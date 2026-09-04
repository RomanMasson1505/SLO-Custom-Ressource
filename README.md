# SLO Operator

[![Lint](https://github.com/RomanMasson1505/SLO-Custom-Ressource/actions/workflows/lint.yml/badge.svg)](https://github.com/RomanMasson1505/SLO-Custom-Ressource/actions/workflows/lint.yml)
[![Tests](https://github.com/RomanMasson1505/SLO-Custom-Ressource/actions/workflows/test.yml/badge.svg)](https://github.com/RomanMasson1505/SLO-Custom-Ressource/actions/workflows/test.yml)

A Kubernetes operator that turns **Service Level Objectives** into native Kubernetes
resources. Declare an SLO once, and the operator generates the Prometheus alerting
rules, continuously evaluates the error budget, and reports the result in the
resource status.

> Status: work in progress. The core is functional end to end (rule generation +
> budget evaluation + admission webhooks). See the [roadmap](#roadmap).

---

## What it does

For every `ServiceLevelObjective` you apply, the operator:

1. **Generates a `PrometheusRule`** containing
   - recording rules for the SLI error ratio across every burn-rate window, and
   - **multi-window, multi-burn-rate** alerts following the
     [Google SRE Workbook](https://sre.google/workbook/alerting-on-slos/).
2. **Evaluates the error budget** by querying Prometheus, and writes the remaining
   budget, the current burn rate and a health phase to the status.
3. **Validates and defaults** every SLO through admission webhooks, so a broken spec
   is rejected before it reaches the cluster.

```mermaid
flowchart LR
    U[kubectl apply SLO] --> W[Admission webhooks<br/>default + validate]
    W --> API[apiserver] --> ETCD[(etcd)]
    ETCD -->|watch| REC[Reconcile]
    REC -->|build + apply| PR[PrometheusRule]
    PR -.ownerRef.-> SLO[ServiceLevelObjective]
    PROM[Prometheus] --> PR
    REC -->|query error ratio| PROM
    REC -->|patch status| ETCD
```

---

## The `ServiceLevelObjective` resource

```yaml
apiVersion: sre.slo.romanmasson.dev/v1alpha1
kind: ServiceLevelObjective
metadata:
  name: checkout-availability
  namespace: shop
spec:
  description: "Checkout API availability"
  objective: "99.9"        # target in percent, kept as a string for precision
  window: 30d              # rolling compliance window (Prometheus duration)
  sli:
    type: availability     # availability | latency
    totalQuery: sum(rate(http_requests_total{service="checkout"}[{{.Window}}]))
    errorQuery: sum(rate(http_requests_total{service="checkout",code=~"5.."}[{{.Window}}]))
```

### Spec fields

| Field | Required | Description |
|---|---|---|
| `objective` | ✅ | Target in percent, e.g. `"99.9"`. String to avoid float rounding. Must be in `(0, 100)`. |
| `window` | | Rolling window (default `30d`). Any valid Prometheus duration. |
| `sli.type` | ✅ | `availability` or `latency`. Immutable after creation. |
| `sli.totalQuery` | ✅ | PromQL for the denominator (all events). May contain `{{.Window}}`. |
| `sli.errorQuery` | ✅ | PromQL for the numerator (bad events). May contain `{{.Window}}`. |
| `enforcement` | | Opt-in. When the budget is exhausted, label the selected Deployments (see [Enforcement](#enforcement)). |

The `{{.Window}}` placeholder is substituted per burn-rate window when the operator
generates the recording rules.

### Status

`kubectl get slo` surfaces the key columns:

```
NAME                   OBJECTIVE   WINDOW   BUDGET-REMAINING   PHASE     AGE
checkout-availability  99.9        30d      87.30              Healthy   2d
```

| Field | Meaning |
|---|---|
| `errorBudgetRemaining` | Remaining error budget in percent (0–100). |
| `currentBurnRate` | How many times faster than nominal the budget is being spent. |
| `phase` | `Healthy` (>25% left), `Warning` (≤25%), `Exhausted` (≤0%), `Unknown` (Prometheus unreachable). |
| `conditions` | `Ready`, `RulesGenerated`, `PrometheusReachable`, `BudgetHealthy`. |

---

## How burn-rate alerting works

The error budget is what you're allowed to fail: a 99.9% objective over 30 days
allows ~43 minutes of downtime per month. The **burn rate** is how fast you spend it
(1 = spend it exactly over the window; 14.4 = spend it all in ~2 days).

Each alert combines a **long** window (confirms the problem is real) and a **short**
window (clears the alert quickly once fixed). Both must exceed `burnRate × budget`:

| Severity | Short window | Long window | Burn rate | Budget spent to fire |
|---|---|---|---|---|
| critical (page) | 5m | 1h | 14.4 | 2% in 1h |
| critical (page) | 30m | 6h | 6 | 5% in 6h |
| warning (ticket) | 2h | 1d | 3 | 10% in 1d |
| warning (ticket) | 6h | 3d | 1 | 10% in 3d |

These factors are calibrated for a 30-day window (Google SRE Workbook).

---

## Enforcement

Enforcement is opt-in per SLO. When enabled and the budget reaches `Exhausted`, the
operator stamps every Deployment matching the selector (in the SLO's namespace) with:

```
slo.io/budget-exhausted: "true"
```

The label is removed automatically once the phase returns to `Healthy`/`Warning`.

```yaml
spec:
  enforcement:
    freezeDeployments: true
    selector:
      matchLabels:
        app: checkout
```

The operator only **sets the label** — it deliberately does not block anything itself.
A cluster admin decides what the label means by applying a policy such as the
`ValidatingAdmissionPolicy` example in
[`config/samples/validatingadmissionpolicy_freeze.yaml`](config/samples/validatingadmissionpolicy_freeze.yaml),
which rejects image changes on frozen Deployments. This keeps *who marks* and *who
blocks* cleanly separated.

---

## Install with Helm

For consumers who just want to run the operator (no clone, no build), a Helm chart
is generated under [`dist/chart`](dist/chart). It installs the CRD, RBAC, the manager
Deployment (pointing at the published GHCR image), and the webhooks in one command.

Prerequisites in the cluster: **cert-manager** (for webhook TLS) and a **Prometheus**
the operator can reach.

```bash
helm install slo-operator ./dist/chart \
  --namespace slo-system --create-namespace \
  --set controllerManager.container.image.tag=v0.1.0 \
  --set-string 'controllerManager.container.args[3]=--prometheus-url=http://<your-prometheus>.svc:9090'
```

Everything is configurable through `dist/chart/values.yaml` (image, replicas,
Prometheus URL, whether to install the CRD, etc.). The chart references the image
published by the release workflow, so no local build is needed.

---

## Quickstart (local cluster)

Try the whole thing end to end on a local cluster (minikube shown; kind works too).
Prerequisites: `docker`, `kubectl`, `helm`, and minikube.

```bash
# 1. a local cluster
minikube start --cpus=2 --memory=3600

# 2. cert-manager (issues the webhook's TLS certificate)
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.16.2/cert-manager.yaml
kubectl wait --for=condition=Available --timeout=180s deployment -n cert-manager --all

# 3. Prometheus (picks up all PrometheusRules and ServiceMonitors)
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts && helm repo update
helm install kps prometheus-community/kube-prometheus-stack \
  --namespace monitoring --create-namespace \
  --set prometheus.prometheusSpec.ruleSelectorNilUsesHelmValues=false \
  --set prometheus.prometheusSpec.serviceMonitorSelectorNilUsesHelmValues=false \
  --set grafana.enabled=false --set alertmanager.enabled=false

# 4. a demo app that exposes a metric (+ ServiceMonitor)
kubectl apply -f hack/e2e/demo-app.yaml

# 5. install the CRD, then build + deploy the operator
make install
make docker-build IMG=slo-operator:dev
minikube image load slo-operator:dev
make deploy IMG=slo-operator:dev

# 6. create a SLO and watch it evaluate the budget
kubectl apply -f hack/e2e/slo.yaml
kubectl get slo -n demo        # PHASE Healthy, BUDGET-REMAINING ~100
```

To see enforcement kick in, inject errors and watch the budget drain:

```bash
kubectl port-forward -n demo svc/checkout 8080:8080 &   # then, in the same shell:
curl "localhost:8080/set-errors?n=5"                    # ~33% error rate
kubectl get slo -n demo -w                               # ~1-2 min -> Exhausted
kubectl get deploy checkout -n demo --show-labels        # slo.io/budget-exhausted=true
```

> The operator reads Prometheus at `--prometheus-url`; the deployed manifest points
> at the `kps` release used above. Adjust it if your Prometheus Service differs.

---

## Getting started (development)

Prerequisites: Go 1.26, `make`, and (for integration tests) the envtest binaries.

```bash
# Run all unit + integration tests (downloads envtest binaries on first run)
make test

# Run the operator locally against your current kubecontext
make run
# ...or point it at a specific Prometheus:
go run ./cmd/main.go --prometheus-url=http://localhost:9090

# Install the CRD into the cluster
make install
```

The operator reads Prometheus at `--prometheus-url`
(default `http://kube-prometheus-stack-prometheus.monitoring.svc:9090`).

### Fast iteration (no cluster needed)

The core logic is pure and testable without a cluster:

```bash
go test ./internal/rules/...                        # rule generation (golden tests)
go test ./internal/controller/ -run TestReconcile   # reconcile logic (fake client)
go test ./internal/webhook/...                      # validation logic
```

---

## Project layout

```
api/v1alpha1/            API types (Spec/Status) + phase & condition constants
internal/rules/          Pure SLO -> PrometheusRule generation (+ golden tests)
internal/promclient/     Thin, mockable Prometheus client (PromAPI interface)
internal/controller/     Reconcile loop + budget/phase computation
internal/webhook/        Defaulting + validating admission webhooks
cmd/main.go              Wiring: flags, scheme, controller, webhooks
```

Design principle: **isolate the pure logic, test it without a cluster, and keep only
orchestration in the controller.**

---

## Roadmap

- [x] API types + CRD (Spec/Status, printer columns, short name `slo`)
- [x] `PrometheusRule` generation (recording rules + burn-rate alerts)
- [x] Apply the rule from the reconcile loop (owner reference, idempotent)
- [x] Error-budget evaluation from Prometheus (budget %, burn rate, phase, events)
- [x] Admission webhooks (defaulting + validation, PromQL parsing, immutability)
- [x] Enforcement: label Deployments when the budget is exhausted (+ VAP example)
- [x] CI: lint + test workflows, and a multi-arch release image to GHCR on tag
- [x] Helm chart for one-command install (`dist/chart`)
- [x] Quickstart on a local cluster (see above)
- [ ] Fuller end-to-end test scenario (the current `test/e2e` is the scaffold)
- [ ] Tag `v0.1.0`

---

## License

Apache-2.0.
