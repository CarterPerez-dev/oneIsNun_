/*
AngelaMos | 2026
metrics.go
*/

package handler

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/carterperez-dev/templates/go-backend/internal/core"
	"github.com/carterperez-dev/templates/go-backend/internal/metrics"
)

type metricsService interface {
	GetDashboardMetrics(ctx context.Context) (*metrics.DashboardMetrics, error)
}

type MetricsHandler struct {
	service metricsService
}

func NewMetricsHandler(service metricsService) *MetricsHandler {
	return &MetricsHandler{service: service}
}

func (h *MetricsHandler) RegisterRoutes(r chi.Router) {
	r.Route("/api/metrics", func(r chi.Router) {
		r.Get("/", h.GetMetrics)
	})
}

func (h *MetricsHandler) GetMetrics(w http.ResponseWriter, r *http.Request) {
	m, err := h.service.GetDashboardMetrics(r.Context())
	if err != nil {
		core.InternalServerError(w, err)
		return
	}

	core.OK(w, m)
}
