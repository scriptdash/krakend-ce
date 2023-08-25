package krakend

import (
	"github.com/luraproject/lura/logging"
	"github.com/luraproject/lura/proxy"
	"github.com/scriptdash/lura-otel/otelgin"
)

// NewProxyFactory returns a new ProxyFactory wrapping the injected BackendFactory with the default proxy stack and a metrics collector
func NewProxyFactory(logger logging.Logger, backendFactory proxy.BackendFactory) proxy.Factory {
	proxyFactory := proxy.NewDefaultFactory(backendFactory, logger)
	proxyFactory = otelgin.NewProxyFactory(proxyFactory)
	return proxyFactory
}

type proxyFactory struct{}

func (p proxyFactory) NewProxyFactory(logger logging.Logger, backendFactory proxy.BackendFactory) proxy.Factory {
	return NewProxyFactory(logger, backendFactory)
}
