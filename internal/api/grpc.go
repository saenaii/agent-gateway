package api

import (
	"context"
	"fmt"
	"time"

	"agent-gateway/internal/api/pb"
	"agent-gateway/internal/intent"
	"agent-gateway/internal/layer2"
	"agent-gateway/internal/metrics"
	"agent-gateway/internal/upstream"
)

// grpcServer implements the internal Gateway gRPC service.
type grpcServer struct {
	pb.UnimplementedGatewayServer
	classifier      *intent.Classifier
	router          *layer2.Router
	metrics         *metrics.Collector
	classifyTimeout time.Duration
}

// NewGRPCServer builds the gRPC service around the shared classifier, Layer 2
// router, and metrics collector. router may be nil when Layer 2 is
// unavailable; classifyTimeout bounds intent classification, a zero value
// defaults to 10s.
func NewGRPCServer(classifier *intent.Classifier, m *metrics.Collector, router *layer2.Router, classifyTimeout ...time.Duration) pb.GatewayServer {
	timeout := 10 * time.Second
	if len(classifyTimeout) > 0 && classifyTimeout[0] > 0 {
		timeout = classifyTimeout[0]
	}
	return &grpcServer{classifier: classifier, router: router, metrics: m, classifyTimeout: timeout}
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
	usage := upstream.Usage{
		PromptTokens:     result.Usage.PromptTokens,
		CompletionTokens: result.Usage.CompletionTokens,
	}
	if layer == 1 {
		content = intent.QuickReply(result.Intent, last.GetContent())
	} else if s.router != nil {
		resp, err := s.router.Chat(ctx, req.GetModel(), layer2.ChatRequest{Messages: toUpstreamMessages(req.Messages)})
		if err != nil {
			return nil, fmt.Errorf("layer2: %w", err)
		}
		content = resp.Content
		usage = resp.Usage
		if usage.PromptTokens > 0 || usage.CompletionTokens > 0 {
			s.metrics.RecordTokens(usage.PromptTokens, usage.CompletionTokens, false)
		}
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
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
	}, nil
}

func toUpstreamMessages(messages []*pb.Message) []upstream.Message {
	out := make([]upstream.Message, 0, len(messages))
	for _, m := range messages {
		out = append(out, upstream.Message{Role: m.GetRole(), Content: m.GetContent()})
	}
	return out
}
