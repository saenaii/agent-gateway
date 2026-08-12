// Command gateway runs the agent gateway: an OpenAI-compatible HTTP API,
// an internal gRPC service, and an embedded observability dashboard.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"agent-gateway/internal/api"
	"agent-gateway/internal/api/pb"
	"agent-gateway/internal/breaker"
	"agent-gateway/internal/config"
	"agent-gateway/internal/intent"
	"agent-gateway/internal/metrics"
	"agent-gateway/internal/pool"
	"agent-gateway/internal/upstream"
	"google.golang.org/grpc"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("gateway exited", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load("config.yaml", ".env")
	if err != nil {
		return err
	}

	collector := metrics.New(cfg.Metrics.Window.Duration, cfg.Metrics.MaxSamples, cfg.Metrics.RequestLogSize)
	breaker := breaker.New(cfg.CircuitBreaker.FailureThreshold, cfg.CircuitBreaker.Cooldown.Duration)
	workerPool := pool.New(cfg.WorkerPool.Size, cfg.WorkerPool.QueueSize)
	defer workerPool.Close()

	ollama := upstream.NewClient(cfg.Ollama.BaseURL, cfg.Ollama.RequestTimeout.Duration)
	classifier := intent.NewClassifier(ollama, cfg.Ollama.Model, cfg.Ollama.RetryAttempts, breaker, workerPool)

	gateway := &api.Gateway{
		Classifier:      classifier,
		Metrics:         collector,
		Dashboard:       cfg.Dashboard.Enabled,
		ClassifyTimeout: classifyBudget(cfg),
		Logger:          logger,
	}

	httpServer := &http.Server{
		Addr:         cfg.Server.HTTPAddr,
		Handler:      gateway.Handler(),
		ReadTimeout:  cfg.Server.ReadTimeout.Duration,
		WriteTimeout: cfg.Server.WriteTimeout.Duration,
	}

	grpcServer := grpc.NewServer()
	pb.RegisterGatewayServer(grpcServer, api.NewGRPCServer(classifier, collector, classifyBudget(cfg)))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 2)
	go func() { errCh <- httpServer.ListenAndServe() }()
	go func() {
		ln, err := net.Listen("tcp", cfg.Server.GRPCAddr)
		if err != nil {
			errCh <- err
			return
		}
		errCh <- grpcServer.Serve(ln)
	}()
	logger.Info("agent-gateway started",
		"http", cfg.Server.HTTPAddr,
		"grpc", cfg.Server.GRPCAddr,
		"ollama", cfg.Ollama.BaseURL,
		"model", cfg.Ollama.Model,
	)

	select {
	case <-ctx.Done():
		logger.Info("shutting down")
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	grpcServer.GracefulStop()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Warn("http shutdown", "err", err)
	}
	return nil
}

// classifyBudget covers all retry attempts plus a margin, so slow local
// models do not get cut off mid-retry.
func classifyBudget(cfg *config.Config) time.Duration {
	return cfg.Ollama.RequestTimeout.Duration*time.Duration(cfg.Ollama.RetryAttempts+1) + 5*time.Second
}
