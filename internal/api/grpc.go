package api

import (
	"context"
	"fmt"
	"time"

	"agent-gateway/internal/api/pb"
	"agent-gateway/internal/intent"
	"agent-gateway/internal/metrics"
)

// grpcServer implements the internal Gateway gRPC service.
type grpcServer struct {
	pb.UnimplementedGatewayServer
	classifier      *intent.Classifier
	metrics         *metrics.Collector
	classifyTimeout time.Duration
}

// NewGRPCServer builds the gRPC service around the shared classifier and
// metrics collector. classifyTimeout bounds intent classification; a zero
// value defaults to 10s.
func NewGRPCServer(classifier *intent.Classifier, m *metrics.Collector, classifyTimeout ...time.Duration) pb.GatewayServer {
	timeout := 10 * time.Second
	if len(classifyTimeout) > 0 && classifyTimeout[0] > 0 {
		timeout = classifyTimeout[0]
	}
	return &grpcServer{classifier: classifier, metrics: m, classifyTimeout: timeout}
}

// Chat routes a chat completion request through the semantic router.
func (s *grpcServer) Chat(ctx context.Context, req *pb.ChatRequest) (*pb.ChatResponse, error) {
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("no messages in request")
	}
	last := req.Messages[len(req.Messages)-1]
	start := time.Now()

	classifyCtx, cancel := context.WithTimeout(ctx, s.classifyTimeout)
	defer cancel()

	result, err := s.classifier.Classify(classifyCtx, last.GetContent())
	if err != nil {
		return nil, fmt.Errorf("classify intent: %w", err)
	}
	if result.Usage.PromptTokens > 0 || result.Usage.CompletionTokens > 0 {
		s.metrics.RecordTokens(result.Usage.PromptTokens, result.Usage.CompletionTokens, true)
	}

	layer := result.Intent.Layer()
	content := ""
	if layer == 1 {
		content = intent.QuickReply(result.Intent, last.GetContent())
	}

	elapsed := time.Since(start)
	s.metrics.RecordRequestResult(layer, elapsed, elapsed)
	s.metrics.RecordRequest(metrics.Request{
		ID:       newID(),
		Time:     start.Format(time.RFC3339),
		Intent:   result.Intent.String(),
		Layer:    layer,
		Status:   "ok",
		TTFT:     float64(elapsed) / float64(time.Millisecond),
		Duration: float64(elapsed) / float64(time.Millisecond),
	})

	return &pb.ChatResponse{
		Id:               newID(),
		Intent:           result.Intent.String(),
		Layer:            int32(layer),
		Content:          content,
		PromptTokens:     result.Usage.PromptTokens,
		CompletionTokens: result.Usage.CompletionTokens,
	}, nil
}
