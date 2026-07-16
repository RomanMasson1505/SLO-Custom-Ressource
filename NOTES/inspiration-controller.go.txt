//go:build ignore

// ⚠️ FICHIER D'INSPIRATION — NE FAIT PAS PARTIE DE LA COMPILATION (//go:build ignore).
// Il redéclare SLOReconciler / Reconcile / SetupWithManager pour te servir de modèle.
// Recopie la logique utile dans le VRAI internal/controller/slo_controller.go,
// puis `make manifests` (pour régénérer le RBAC à partir des markers +kubebuilder:rbac).
//
// Idée générale d'un contrôleur (boucle de réconciliation) :
//   1. Charger l'objet SLO demandé (req.NamespacedName)
//   2. Interroger Prometheus : conformité sur Window + tendance sur PredictionWindow
//   3. Comparer à la Target -> calculer SLOBreached et PredictedBreach
//   4. Écrire le résultat dans Status (+ Conditions)
//   5. Re-planifier (RequeueAfter) pour re-vérifier régulièrement

/*
Copyright 2026.
Licensed under the Apache License, Version 2.0 (the "License");
*/

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	batchv1 "github.com/RomanMasson1505/SLO-Custom-Ressource/api/v1"
)

// SLOReconciler reconciles a SLO object
type SLOReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// HTTPClient permet d'injecter un client (et de le mocker en test). Optionnel.
	HTTPClient *http.Client
}

// Markers RBAC : `make manifests` les transforme en config/rbac/role.yaml.
// On a besoin de lire/écrire nos SLO et de mettre à jour leur status.
// +kubebuilder:rbac:groups=batch.slo.operator.com,resources=slos,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch.slo.operator.com,resources=slos/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=batch.slo.operator.com,resources=slos/finalizers,verbs=update

// Types de Conditions qu'on expose dans le Status.
const (
	conditionAvailable  = "Available"  // le contrôleur a pu évaluer le SLO
	conditionBreached   = "SLOBreached" // SLO sous la target
	conditionPredicted  = "PredictedBreach" // dépassement prévu
)

func (r *SLOReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// --- 1. Charger l'objet SLO -------------------------------------------------
	var slo batchv1.SLO
	if err := r.Get(ctx, req.NamespacedName, &slo); err != nil {
		// IsNotFound = l'objet a été supprimé : on arrête sans erreur.
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// --- 2. Parser les paramètres ----------------------------------------------
	target, err := strconv.ParseFloat(slo.Spec.Target, 64)
	if err != nil {
		log.Error(err, "target invalide", "target", slo.Spec.Target)
		// Pas la peine de requeue immédiatement : il faut que l'utilisateur corrige.
		return ctrl.Result{}, nil
	}

	pollInterval := 60 * time.Second
	if slo.Spec.PollingInterval != "" {
		if d, perr := time.ParseDuration(slo.Spec.PollingInterval); perr == nil {
			pollInterval = d
		}
	}

	// --- 3. Interroger Prometheus ----------------------------------------------
	// (a) Conformité actuelle sur la fenêtre globale Window.
	//     On enveloppe la métrique de l'utilisateur dans un avg_over_time
	//     pour obtenir la moyenne du ratio de succès sur toute la fenêtre.
	currentQuery := fmt.Sprintf("avg_over_time((%s)[%s:])", slo.Spec.Metric, slo.Spec.Window)
	currentRatio, err := r.queryPrometheus(ctx, slo.Spec.PrometheusAddress, currentQuery)
	if err != nil {
		log.Error(err, "échec requête Prometheus (current)")
		r.setCondition(&slo, conditionAvailable, metav1.ConditionFalse, "QueryFailed", err.Error())
		_ = r.Status().Update(ctx, &slo)
		// On retente plus tard.
		return ctrl.Result{RequeueAfter: pollInterval}, nil
	}

	// (b) Tendance récente sur la sous-fenêtre de prédiction.
	recentQuery := fmt.Sprintf("avg_over_time((%s)[%s:])", slo.Spec.Metric, slo.Spec.PredictionWindow)
	recentRatio, err := r.queryPrometheus(ctx, slo.Spec.PrometheusAddress, recentQuery)
	if err != nil {
		log.Error(err, "échec requête Prometheus (prediction)")
		recentRatio = currentRatio // dégradé : on retombe sur la valeur courante
	}

	// --- 4. Calculs SLO ---------------------------------------------------------
	currentSLO := currentRatio * 100.0 // ratio 0..1 -> pourcentage

	// Budget d'erreur : combien d'indisponibilité on s'autorise = (100 - target)%.
	// Restant = (current - target) / (100 - target). 1 => intact, <=0 => épuisé.
	errorBudgetAllowed := 100.0 - target
	budgetRemaining := 100.0
	if errorBudgetAllowed > 0 {
		budgetRemaining = ((currentSLO - target) / errorBudgetAllowed) * 100.0
	}

	// SORTIE 1 : le SLO est-il dépassé maintenant ?
	sloBreached := currentSLO < target

	// SORTIE 2 : prédiction. Idée simple :
	//   On suppose que la tendance récente (recentRatio) va continuer.
	//   Si la conformité récente est déjà sous la target, on prévoit un dépassement.
	//   (Version plus fine possible : extrapoler la consommation du budget linéairement.)
	predictedSLO := recentRatio * 100.0
	predictedBreach := !sloBreached && predictedSLO < target

	// --- 5. Écrire le Status ----------------------------------------------------
	slo.Status.CurrentSLO = fmt.Sprintf("%.4f", currentSLO)
	slo.Status.ErrorBudgetRemaining = fmt.Sprintf("%.2f", budgetRemaining)
	slo.Status.SLOBreached = sloBreached
	slo.Status.PredictedBreach = predictedBreach
	now := metav1.Now()
	slo.Status.LastEvaluationTime = &now

	r.setCondition(&slo, conditionAvailable, metav1.ConditionTrue, "Evaluated", "SLO évalué avec succès")

	if sloBreached {
		r.setCondition(&slo, conditionBreached, metav1.ConditionTrue, "BelowTarget",
			fmt.Sprintf("SLO %.4f%% < target %.2f%%", currentSLO, target))
	} else {
		r.setCondition(&slo, conditionBreached, metav1.ConditionFalse, "WithinTarget", "SLO respecté")
	}

	if predictedBreach {
		r.setCondition(&slo, conditionPredicted, metav1.ConditionTrue, "TrendNegative",
			fmt.Sprintf("tendance récente %.4f%% sous la target", predictedSLO))
	} else {
		r.setCondition(&slo, conditionPredicted, metav1.ConditionFalse, "TrendOK", "pas de dépassement prévu")
	}

	if err := r.Status().Update(ctx, &slo); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("SLO évalué", "current", currentSLO, "target", target,
		"breached", sloBreached, "predictedBreach", predictedBreach)

	// --- 6. Re-planifier la prochaine évaluation -------------------------------
	return ctrl.Result{RequeueAfter: pollInterval}, nil
}

// setCondition : petit helper pour mettre à jour une Condition dans le Status.
func (r *SLOReconciler) setCondition(slo *batchv1.SLO, condType string, status metav1.ConditionStatus, reason, msg string) {
	meta.SetStatusCondition(&slo.Status.Conditions, metav1.Condition{
		Type:    condType,
		Status:  status,
		Reason:  reason,
		Message: msg,
	})
}

// ---------------------------------------------------------------------------------
// Appel HTTP à l'API Prometheus : GET /api/v1/query?query=...
// Réponse JSON typique :
// { "status":"success",
//   "data": { "resultType":"vector",
//             "result":[ { "metric":{...}, "value":[<timestamp>, "<valeur>"] } ] } }
// ---------------------------------------------------------------------------------

type promResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Value [2]interface{} `json:"value"` // [timestamp(float), value(string)]
		} `json:"result"`
	} `json:"data"`
	Error string `json:"error"`
}

// queryPrometheus exécute une requête instantanée et renvoie la 1ère valeur scalaire.
func (r *SLOReconciler) queryPrometheus(ctx context.Context, address, query string) (float64, error) {
	hc := r.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}

	endpoint := fmt.Sprintf("%s/api/v1/query?query=%s", address, url.QueryEscape(query))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}

	resp, err := hc.Do(httpReq)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("prometheus a répondu %d", resp.StatusCode)
	}

	var pr promResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return 0, err
	}
	if pr.Status != "success" {
		return 0, fmt.Errorf("erreur prometheus: %s", pr.Error)
	}
	if len(pr.Data.Result) == 0 {
		return 0, fmt.Errorf("aucun résultat pour la requête: %s", query)
	}

	// value[1] est la valeur sous forme de string -> on parse en float.
	valStr, ok := pr.Data.Result[0].Value[1].(string)
	if !ok {
		return 0, fmt.Errorf("format de valeur inattendu")
	}
	return strconv.ParseFloat(valStr, 64)
}

// SetupWithManager : on déclenche une réconciliation à chaque changement d'un SLO.
// (Pas de Owns() ici car on ne crée pas de ressources enfant ; on lit Prometheus.)
func (r *SLOReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&batchv1.SLO{}).
		Named("slo").
		Complete(r)
}
