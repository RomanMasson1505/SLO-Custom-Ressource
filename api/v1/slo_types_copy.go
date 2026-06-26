//go:build ignore

// ⚠️ FICHIER D'INSPIRATION — NE FAIT PAS PARTIE DE LA COMPILATION.
// La directive `//go:build ignore` ci-dessus exclut ce fichier du build et de `make`.
// Il redéclare volontairement SLOSpec/SLOStatus/SLO pour te servir de modèle :
// recopie le contenu utile dans le VRAI fichier api/v1/slo_types.go, puis lance `make generate manifests`.
//
// Objectif du CRD (rappel) :
//   Entrées (Spec)  : podLabel, métrique Prometheus, target SLO, fenêtre SLO, sous-fenêtre de prédiction
//   Sorties (Status): alerte SLO dépassée (oui/non), alerte prédiction (oui/non)

/*
Copyright 2026.
Licensed under the Apache License, Version 2.0 (the "License");
*/

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// =====================================================================================
// SPEC — l'état DÉSIRÉ, c'est ce que l'utilisateur écrit dans son YAML.
// =====================================================================================

// SLOSpec définit la configuration d'un SLO à surveiller.
type SLOSpec struct {
	// PodLabels : les labels qui identifient les pods/le service concerné.
	// Sert à filtrer la métrique Prometheus (ex: {"app":"my-api"}).
	// On utilise une map[string]string : c'est un label selector simple.
	// +required
	PodLabels map[string]string `json:"podLabels"`

	// PrometheusAddress : URL de base du serveur Prometheus à interroger.
	// Ex: "http://prometheus-operated.monitoring.svc:9090"
	// +required
	PrometheusAddress string `json:"prometheusAddress"`

	// Metric : la requête PromQL (ou le nom de métrique) qui exprime le "ratio de bons événements".
	// Le résultat attendu est un ratio entre 0 et 1 (1 = 100% de succès).
	// Tu peux y injecter tes labels via $LABELS dans le contrôleur, ou écrire la requête complète.
	// Ex: `sum(rate(http_requests_total{status!~"5.."}[5m])) / sum(rate(http_requests_total[5m]))`
	// +required
	Metric string `json:"metric"`

	// Target : l'objectif SLO en pourcentage (ex: 99.9 pour 99.9%).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +required
	Target string `json:"target"`
	// NB: on prend un string ici pour garder la précision décimale (99.95).
	// Alternative pédagogique : resource.Quantity ou un *int32 si entier suffit.
	// Pour rester simple en débutant tu peux aussi mettre :  Target string `json:"target"`
	// et le parser avec strconv.ParseFloat dans le contrôleur.

	// Window : la fenêtre globale d'évaluation du SLO (format durée Prometheus).
	// Ex: "30d", "7d", "24h". C'est sur cette période qu'on calcule la conformité.
	// +kubebuilder:validation:Pattern=`^[0-9]+(s|m|h|d|w)$`
	// +required
	Window string `json:"window"`

	// PredictionWindow : la SOUS-fenêtre récente utilisée pour la prédiction.
	// On regarde la tendance récente (ex: "1h") et on extrapole sur Window.
	// Ex: "1h", "6h".
	// +kubebuilder:validation:Pattern=`^[0-9]+(s|m|h|d|w)$`
	// +required
	PredictionWindow string `json:"predictionWindow"`

	// PollingInterval : à quelle fréquence le contrôleur ré-interroge Prometheus.
	// Optionnel, défaut géré dans le contrôleur (ex: 60s).
	// +optional
	PollingInterval string `json:"pollingInterval,omitempty"`
}

// =====================================================================================
// STATUS — l'état OBSERVÉ, c'est le contrôleur qui le remplit.
// =====================================================================================

// SLOStatus reflète ce que le contrôleur a calculé à partir de Prometheus.
type SLOStatus struct {
	// CurrentSLO : la conformité mesurée sur Window (en %, ex: 99.92).
	// +optional
	CurrentSLO string `json:"currentSLO,omitempty"`

	// ErrorBudgetRemaining : budget d'erreur restant en % (100% = intact, 0% = épuisé).
	// +optional
	ErrorBudgetRemaining string `json:"errorBudgetRemaining,omitempty"`

	// SLOBreached : true si le SLO mesuré est SOUS la target (objectif non tenu).
	// C'est la première sortie demandée : "alerte ou non SLO dépassée".
	// +optional
	SLOBreached bool `json:"sloBreached"`

	// PredictedBreach : true si, en extrapolant la tendance récente
	// (PredictionWindow) sur Window, on prévoit un dépassement.
	// C'est la deuxième sortie : "alerte ou non prédiction".
	// +optional
	PredictedBreach bool `json:"predictedBreach"`

	// LastEvaluationTime : horodatage de la dernière interrogation Prometheus réussie.
	// +optional
	LastEvaluationTime *metav1.Time `json:"lastEvaluationTime,omitempty"`

	// Conditions : conventions Kubernetes pour exposer l'état de façon standard.
	// On y mettra des types comme "Available", "SLOBreached", "PredictedBreach".
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// =====================================================================================
// Types racine — généralement on n'y touche pas (sauf les markers printcolumn).
// =====================================================================================

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// Ces "printcolumn" affichent des colonnes utiles dans `kubectl get slo` :
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.target`
// +kubebuilder:printcolumn:name="Current",type=string,JSONPath=`.status.currentSLO`
// +kubebuilder:printcolumn:name="Breached",type=boolean,JSONPath=`.status.sloBreached`
// +kubebuilder:printcolumn:name="PredBreach",type=boolean,JSONPath=`.status.predictedBreach`

// SLO is the Schema for the slos API
type SLO struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`
	// +required
	Spec SLOSpec `json:"spec"`
	// +optional
	Status SLOStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// SLOList contains a list of SLO
type SLOList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []SLO `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &SLO{}, &SLOList{})
		return nil
	})
}
