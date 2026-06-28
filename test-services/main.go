package main

import (
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var requests = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "fake_api_http_requests_total",
		Help: "Total de requêtes simulées, labellisées par statut (success/error)",
	},
	[]string{"status"},
)

func main() {
	prometheus.MustRegister(requests)

	// ERROR_RATE : float entre 0.0 et 1.0 (ex: 0.3 = 30% d'erreurs)
	errorRate, _ := strconv.ParseFloat(os.Getenv("ERROR_RATE"), 64)

	// REQUESTS_PER_SECOND : nb de requêtes simulées par seconde (défaut: 10)
	rps := 10.0
	if v := os.Getenv("REQUESTS_PER_SECOND"); v != "" {
		if parsed, err := strconv.ParseFloat(v, 64); err == nil {
			rps = parsed
		}
	}

	// Goroutine qui génère du trafic synthétique en continu
	// (pas besoin de curl externe pour alimenter les métriques)
	go func() {
		interval := time.Duration(float64(time.Second) / rps)
		for {
			if rand.Float64() < errorRate {
				requests.WithLabelValues("error").Inc()
			} else {
				requests.WithLabelValues("success").Inc()
			}
			time.Sleep(interval)
		}
	}()

	// Endpoint HTTP réel (optionnel, pour tester manuellement)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if rand.Float64() < errorRate {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	http.Handle("/metrics", promhttp.Handler())
	http.ListenAndServe(":8080", nil)
}
