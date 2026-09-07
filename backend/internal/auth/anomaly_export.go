package auth

import (
	"log/slog"
	"time"

	"foldex/internal/auth/anomaly"
)

type Anomaly = anomaly.Anomaly
type AnomalyThresholds = anomaly.Thresholds

const (
	AnomalyKindSpray        = anomaly.KindSpray
	AnomalyKindHammer       = anomaly.KindHammer
	AnomalyKindThrottle     = anomaly.KindThrottle
	AnomalySeverityCritical = anomaly.SeverityCritical
	AnomalySeverityWarn     = anomaly.SeverityWarning
	maxAnomalyRows          = anomaly.MaxRows
	anomalyQueryLimit       = anomaly.QueryLimit
)

func anomalyWindow(raw string, policyMinutes int) (time.Duration, string, bool) {
	return anomaly.Window(raw, policyMinutes)
}

func anomalySeverity(value, threshold int) string {
	return anomaly.SeverityFor(value, threshold)
}

func rankAnomalies(in []Anomaly, limit int, log *slog.Logger) []Anomaly {
	return anomaly.Rank(in, limit, log)
}
