package krakend

import (
	"context"

	httpcache "github.com/devopsfaith/krakend-httpcache"
	martian "github.com/devopsfaith/krakend-martian"
	metrics "github.com/devopsfaith/krakend-metrics/gin"
	oauth2client "github.com/devopsfaith/krakend-oauth2-clientcredentials"
	"github.com/luraproject/lura/config"
	"github.com/luraproject/lura/logging"
	"github.com/luraproject/lura/proxy"
	"github.com/luraproject/lura/transport/http/client"
	httprequestexecutor "github.com/luraproject/lura/transport/http/client/plugin"
	opencensus "github.com/scriptdash/krakend-opencensus"
)

// NewBackendFactory creates a BackendFactory by stacking all the available middlewares:
// - oauth2 client credentials
// - http cache
// - martian
// - pubsub
// - amqp
// - cel
// - lua
// - rate-limit
// - circuit breaker
// - metrics collector
// - opencensus collector
func NewBackendFactory(logger logging.Logger, metricCollector *metrics.Metrics) proxy.BackendFactory {
	return NewBackendFactoryWithContext(context.Background(), logger, metricCollector)
}

// NewBackendFactory creates a BackendFactory by stacking all the available middlewares and injecting the received context
func NewBackendFactoryWithContext(ctx context.Context, logger logging.Logger, metricCollector *metrics.Metrics) proxy.BackendFactory {
	requestExecutorFactory := func(cfg *config.Backend) client.HTTPRequestExecutor {
		var clientFactory client.HTTPClientFactory
		if _, ok := cfg.ExtraConfig[oauth2client.Namespace]; ok {
			clientFactory = oauth2client.NewHTTPClient(cfg)
		} else {
			clientFactory = httpcache.NewHTTPClient(cfg)
		}
		return opencensus.HTTPRequestExecutorFromConfig(clientFactory, cfg)
	}
	requestExecutorFactory = httprequestexecutor.HTTPRequestExecutor(logger, requestExecutorFactory)
	backendFactory := martian.NewConfiguredBackendFactory(logger, requestExecutorFactory)
	backendFactory = metricCollector.BackendFactory("backend", backendFactory)
	return backendFactory
}

type backendFactory struct{}

func (b backendFactory) NewBackendFactory(ctx context.Context, l logging.Logger, m *metrics.Metrics) proxy.BackendFactory {
	return NewBackendFactoryWithContext(ctx, l, m)
}
