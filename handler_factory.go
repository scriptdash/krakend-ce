package krakend

import (
	metrics "github.com/devopsfaith/krakend-metrics/gin"
	"github.com/luraproject/lura/logging"
	router "github.com/luraproject/lura/router/gin"
	"github.com/scriptdash/lura-otel/otelgin"
)

// NewHandlerFactory returns a HandlerFactory with a rate-limit and a metrics collector middleware injected
func NewHandlerFactory(logger logging.Logger, metricCollector *metrics.Metrics) router.HandlerFactory {
	handlerFactory := router.EndpointHandler
	handlerFactory = metricCollector.NewHTTPHandlerFactory(handlerFactory)
	handlerFactory = otelgin.NewHandlerFactory(handlerFactory)
	return handlerFactory
}

type handlerFactory struct{}

func (h handlerFactory) NewHandlerFactory(l logging.Logger, m *metrics.Metrics) router.HandlerFactory {
	return NewHandlerFactory(l, m)
}
