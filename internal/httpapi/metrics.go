package httpapi

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/shaikn6/ledger-service/internal/ledger"
)

// Business metrics, alongside the HTTP metrics in middleware.go.
var (
	metricAccountsCreated = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ledger_accounts_created_total",
		Help: "Accounts opened.",
	})

	metricTransfers = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ledger_transfers_total",
		Help: "Transfers by result (posted, reversed, rejected).",
	}, []string{"result"})

	metricTransferAmount = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "ledger_transfer_amount_minor_units",
		Help:    "Distribution of posted transfer amounts in minor units.",
		Buckets: []float64{100, 1_000, 10_000, 100_000, 1_000_000, 10_000_000},
	})
)

func recordTransfer(t ledger.Transfer) {
	result := "posted"
	if t.ReversesTransferID != nil {
		result = "reversed"
	}
	metricTransfers.WithLabelValues(result).Inc()
	metricTransferAmount.Observe(float64(t.Amount))
}
