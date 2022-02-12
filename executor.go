package krakend

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	cmd "github.com/devopsfaith/krakend-cobra"
	cors "github.com/devopsfaith/krakend-cors/gin"
	gelf "github.com/devopsfaith/krakend-gelf"
	gologging "github.com/devopsfaith/krakend-gologging"
	influxdb "github.com/devopsfaith/krakend-influx"
	logstash "github.com/devopsfaith/krakend-logstash"
	metrics "github.com/devopsfaith/krakend-metrics/gin"
	pubsub "github.com/devopsfaith/krakend-pubsub"
	"github.com/devopsfaith/krakend-usage/client"
	"github.com/gin-gonic/gin"
	"github.com/go-contrib/uuid"
	"github.com/luraproject/lura/config"
	"github.com/luraproject/lura/core"
	"github.com/luraproject/lura/logging"
	"github.com/luraproject/lura/proxy"
	krakendrouter "github.com/luraproject/lura/router"
	router "github.com/luraproject/lura/router/gin"
	server "github.com/luraproject/lura/transport/http/server/plugin"
	opencensus "github.com/scriptdash/krakend-opencensus"
	_ "github.com/scriptdash/krakend-opencensus/exporter/datadog"
	_ "github.com/scriptdash/krakend-opencensus/exporter/influxdb"
	_ "github.com/scriptdash/krakend-opencensus/exporter/jaeger"
	_ "github.com/scriptdash/krakend-opencensus/exporter/ocagent"
	_ "github.com/scriptdash/krakend-opencensus/exporter/prometheus"
	_ "github.com/scriptdash/krakend-opencensus/exporter/stackdriver"
	_ "github.com/scriptdash/krakend-opencensus/exporter/xray"
	_ "github.com/scriptdash/krakend-opencensus/exporter/zipkin"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/jaeger"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/trace"
)

// NewExecutor returns an executor for the cmd package. The executor initalizes the entire gateway by
// registering the components and composing a RouterFactory wrapping all the middlewares.
func NewExecutor(ctx context.Context) cmd.Executor {
	eb := new(ExecutorBuilder)
	return eb.NewCmdExecutor(ctx)
}

// PluginLoader defines the interface for the collaborator responsible of starting the plugin loaders
type PluginLoader interface {
	Load(folder, pattern string, logger logging.Logger)
}

// SubscriberFactoriesRegister registers all the required subscriber factories from the available service
// discover components and adapters and returns a service register function.
// The service register function will register the service by the given name and port to all the available
// service discover clients
type SubscriberFactoriesRegister interface {
	Register(context.Context, config.ServiceConfig, logging.Logger) func(string, int)
}

// MetricsAndTracesRegister registers the defined observability components and returns a metrics collector,
// if required.
type MetricsAndTracesRegister interface {
	Register(context.Context, config.ServiceConfig, logging.Logger) *metrics.Metrics
}

// EngineFactory returns a gin engine, ready to be passed to the KrakenD RouterFactory
type EngineFactory interface {
	NewEngine(config.ServiceConfig, logging.Logger, io.Writer) *gin.Engine
}

// ProxyFactory returns a KrakenD proxy factory, ready to be passed to the KrakenD RouterFactory
type ProxyFactory interface {
	NewProxyFactory(logging.Logger, proxy.BackendFactory, *metrics.Metrics) proxy.Factory
}

// BackendFactory returns a KrakenD backend factory, ready to be passed to the KrakenD proxy factory
type BackendFactory interface {
	NewBackendFactory(context.Context, logging.Logger, *metrics.Metrics) proxy.BackendFactory
}

// HandlerFactory returns a KrakenD router handler factory, ready to be passed to the KrakenD RouterFactory
type HandlerFactory interface {
	NewHandlerFactory(logging.Logger, *metrics.Metrics) router.HandlerFactory
}

// LoggerFactory returns a KrakenD Logger factory, ready to be passed to the KrakenD RouterFactory
type LoggerFactory interface {
	NewLogger(config.ServiceConfig) (logging.Logger, io.Writer, error)
}

// RunServer defines the interface of a function used by the KrakenD router to start the service
type RunServer func(context.Context, config.ServiceConfig, http.Handler) error

// RunServerFactory returns a RunServer with several wraps around the injected one
type RunServerFactory interface {
	NewRunServer(logging.Logger, router.RunServerFunc) RunServer
}

// ExecutorBuilder is a composable builder. Every injected property is used by the NewCmdExecutor method.
type ExecutorBuilder struct {
	LoggerFactory               LoggerFactory
	PluginLoader                PluginLoader
	SubscriberFactoriesRegister SubscriberFactoriesRegister
	MetricsAndTracesRegister    MetricsAndTracesRegister
	EngineFactory               EngineFactory
	ProxyFactory                ProxyFactory
	BackendFactory              BackendFactory
	HandlerFactory              HandlerFactory
	RunServerFactory            RunServerFactory

	Middlewares []gin.HandlerFunc
}

// NewCmdExecutor returns an executor for the cmd package. The executor initalizes the entire gateway by
// delegating most of the tasks to the injected collaborators. They register the components and
// compose a RouterFactory wrapping all the middlewares.
// Every nil collaborator is replaced by the default one offered by this package.
func (e *ExecutorBuilder) NewCmdExecutor(ctx context.Context) cmd.Executor {
	e.checkCollaborators()

	e.Middlewares = []gin.HandlerFunc{
		otelgin.Middleware("krakend"),
	}

	return func(cfg config.ServiceConfig) {
		logger, gelfWriter, gelfErr := e.LoggerFactory.NewLogger(cfg)
		if gelfErr != nil {
			return
		}

		logger.Info("Listening on port:", cfg.Port)
		initTracer(logger, cfg)
		startReporter(ctx, logger, cfg)

		if cfg.Plugin != nil {
			e.PluginLoader.Load(cfg.Plugin.Folder, cfg.Plugin.Pattern, logger)
		}

		metricCollector := e.MetricsAndTracesRegister.Register(ctx, cfg, logger)

		// setup the krakend router
		routerFactory := router.NewFactory(router.Config{
			Engine: e.EngineFactory.NewEngine(cfg, logger, gelfWriter),
			ProxyFactory: e.ProxyFactory.NewProxyFactory(
				logger,
				e.BackendFactory.NewBackendFactory(ctx, logger, metricCollector),
				metricCollector,
			),
			Middlewares:    e.Middlewares,
			Logger:         logger,
			HandlerFactory: e.HandlerFactory.NewHandlerFactory(logger, metricCollector),
			RunServer:      router.RunServerFunc(e.RunServerFactory.NewRunServer(logger, krakendrouter.RunServer)),
		})

		// start the engines
		routerFactory.NewWithContext(ctx).Run(cfg)
	}
}

func (e *ExecutorBuilder) checkCollaborators() {
	if e.PluginLoader == nil {
		e.PluginLoader = new(pluginLoader)
	}
	if e.SubscriberFactoriesRegister == nil {
		e.SubscriberFactoriesRegister = new(registerSubscriberFactories)
	}
	if e.MetricsAndTracesRegister == nil {
		e.MetricsAndTracesRegister = new(MetricsAndTraces)
	}
	if e.EngineFactory == nil {
		e.EngineFactory = new(engineFactory)
	}
	if e.ProxyFactory == nil {
		e.ProxyFactory = new(proxyFactory)
	}
	if e.BackendFactory == nil {
		e.BackendFactory = new(backendFactory)
	}
	if e.HandlerFactory == nil {
		e.HandlerFactory = new(handlerFactory)
	}
	if e.LoggerFactory == nil {
		e.LoggerFactory = new(LoggerBuilder)
	}
	if e.RunServerFactory == nil {
		e.RunServerFactory = new(DefaultRunServerFactory)
	}
}

// DefaultRunServerFactory creates the default RunServer by wrapping the injected RunServer
// with the plugin loader and the CORS module
type DefaultRunServerFactory struct{}

func (d *DefaultRunServerFactory) NewRunServer(l logging.Logger, next router.RunServerFunc) RunServer {
	return RunServer(server.New(
		l,
		server.RunServer(cors.NewRunServer(cors.NewRunServerWithLogger(cors.RunServer(next), l))),
	))
}

// LoggerBuilder is the default BuilderFactory implementation.
type LoggerBuilder struct{}

// NewLogger sets up the logging components as defined at the configuration.
func (LoggerBuilder) NewLogger(cfg config.ServiceConfig) (logging.Logger, io.Writer, error) {
	var writers []io.Writer
	gelfWriter, gelfErr := gelf.NewWriter(cfg.ExtraConfig)
	if gelfErr == nil {
		writers = append(writers, gelfWriterWrapper{gelfWriter})
		gologging.SetFormatterSelector(func(w io.Writer) string {
			switch w.(type) {
			case gelfWriterWrapper:
				return "%{message}"
			default:
				return gologging.DefaultPattern
			}
		})
	}
	logger, gologgingErr := logstash.NewLogger(cfg.ExtraConfig)

	if gologgingErr != nil {
		logger, gologgingErr = gologging.NewLogger(cfg.ExtraConfig, writers...)

		if gologgingErr != nil {
			var err error
			logger, err = logging.NewLogger("DEBUG", os.Stdout, "")
			if err != nil {
				return logger, gelfWriter, err
			}
			logger.Error("unable to create the gologging logger:", gologgingErr.Error())
		}
	}
	if gelfErr != nil {
		logger.Error("unable to create the GELF writer:", gelfErr.Error())
	}
	return logger, gelfWriter, nil
}

// MetricsAndTraces is the default implementation of the MetricsAndTracesRegister interface.
type MetricsAndTraces struct{}

// Register registers the metrcis, influx and opencensus packages as required by the given configuration.
func (MetricsAndTraces) Register(ctx context.Context, cfg config.ServiceConfig, l logging.Logger) *metrics.Metrics {
	metricCollector := metrics.New(ctx, cfg.ExtraConfig, l)

	if err := influxdb.New(ctx, cfg.ExtraConfig, metricCollector, l); err != nil {
		l.Warning(err.Error())
	}

	if err := opencensus.Register(ctx, cfg, append(opencensus.DefaultViews, pubsub.OpenCensusViews...)...); err != nil {
		l.Warning("opencensus:", err.Error())
	}

	return metricCollector
}

const (
	usageDisable = "USAGE_DISABLE"
	usageDelay   = 5 * time.Second
)

func startReporter(ctx context.Context, logger logging.Logger, cfg config.ServiceConfig) {
	if os.Getenv(usageDisable) == "1" {
		logger.Info("usage report client disabled")
		return
	}

	clusterID, err := cfg.Hash()
	if err != nil {
		logger.Warning("unable to hash the service configuration:", err.Error())
		return
	}

	go func() {
		time.Sleep(usageDelay)

		serverID := uuid.NewV4().String()
		logger.Info(fmt.Sprintf("registering usage stats for cluster ID '%s'", clusterID))

		if err := client.StartReporter(ctx, client.Options{
			ClusterID: clusterID,
			ServerID:  serverID,
			Version:   core.KrakendVersion,
		}); err != nil {
			logger.Warning("unable to create the usage report client:", err.Error())
		}
	}()
}

func initTracer(logger logging.Logger, cfg config.ServiceConfig) *trace.TracerProvider {
	extraCfg, ok := cfg.ExtraConfig["github_com/scriptdash/krakend-ce"].(map[string]interface{})
	if !ok {
		logger.Info("skipping tracer initialization: no config found")
		return nil
	}
	exporterCfg, ok := extraCfg["exporters"].(map[string]interface{})
	if !ok {
		logger.Info("skipping tracer initialization: no exporter config found")
	}

	jaegerCfg, ok := exporterCfg["jaeger"].(map[string]interface{})
	if !ok {
		logger.Info("skipping tracer initialization: jaeger config not found")
		return nil
	}
	endpoint, ok := jaegerCfg["endpoint"].(string)
	if !ok {
		logger.Info("skipping tracer initialization: jaeger endpoint not found")
	}

	exporter, err := jaeger.New(jaeger.WithCollectorEndpoint(jaeger.WithEndpoint(endpoint)))
	if err != nil {
		logger.Fatal(err)
	}

	tp := trace.NewTracerProvider(
		trace.WithSampler(trace.AlwaysSample()),
		trace.WithBatcher(exporter),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	return tp
}

type gelfWriterWrapper struct {
	io.Writer
}
