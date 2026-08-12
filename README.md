# Agent Gateway

A high-performance LLM Gateway in Go: a smart semantic router and observability proxy between clients and LLM providers, with a 2-layer routing pipeline.

## Architecture

- **Layer 1 (Intent Classifier)** — routes every prompt to a local small LLM (Ollama, `qwen2.5:0.5b`) that returns a structured JSON intent: `INTENT_GREETING`, `INTENT_TRIVIAL`, or `INTENT_COMPLEX`.
  - `GREETING`/`TRIVIAL` → instant quick reply (zero online token cost).
  - `COMPLEX` → escalated to Layer 2 (not implemented yet; returns an empty response and is tracked as an escalation metric).
  - If Ollama is down or slow, a keyword heuristic fallback keeps the gateway functional.
- **Layer 2 (Commercial/Online LLM)** — planned: upstream DeepSeek/Claude/OpenAI calls with tool calling, circuit breaker, and retries. The gRPC service, SSE parser, and breaker primitives are already in place for it.

## Features

- OpenAI-compatible `POST /v1/chat/completions` (JSON + SSE streaming).
- Internal gRPC `Gateway.Chat` endpoint for microservices.
- Worker pool with context-deadline propagation for Layer 1 calls.
- Circuit breaker + retry around upstream calls.
- Thread-safe rolling-window metrics: token spend, offload/escalation ratios, TTFT, duration, P50/P90/P99.
- Embedded dashboard (`/dashboard`): traffic-split doughnut, token line chart, stat cards, live request log — fully self-contained (Tailwind + Chart.js served locally, no CDN required).
- Live request log endpoint at `/requests`, metrics at `/metrics`, health at `/healthz`.

## Quick start

```sh
cp config.example.yaml config.yaml   # optional; defaults work out of the box
cp .env.example .env                 # optional; env vars override YAML

make build
make run
```

Or with Docker:

```sh
make docker-up            # full stack: gateway + Ollama (qwen2.5:0.5b)
make docker-up-gateway    # gateway only; point OLLAMA_BASE_URL at an external Ollama
```

Notes:

- The `ollama` service is behind the compose `full` profile, so the gateway can start standalone without pulling the Ollama image.
- `OLLAMA_BASE_URL` / `OLLAMA_MODEL` in `docker-compose.yml` honor shell environment overrides, e.g.:

  ```sh
  OLLAMA_BASE_URL=http://host.docker.internal:11434 OLLAMA_MODEL=gemma4:e4b \
    docker-compose up -d gateway
  ```

Then:

```sh
curl -s localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"gateway","messages":[{"role":"user","content":"hello"}]}'

# streaming
curl -sN localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"gateway","stream":true,"messages":[{"role":"user","content":"hi"}]}'

open http://localhost:8080/dashboard
```

## Configuration

Precedence (low → high): built-in defaults → `config.yaml` → `.env` → environment variables. See `config.example.yaml` and `.env.example` for all options.

| Env var | Meaning | Default |
|---|---|---|
| `GATEWAY_HTTP_ADDR` | REST/dashboard listen address | `:8080` |
| `GATEWAY_GRPC_ADDR` | gRPC listen address | `:9090` |
| `OLLAMA_BASE_URL` | Ollama endpoint | `http://localhost:11434` |
| `OLLAMA_MODEL` | Layer 1 classifier model | `qwen2.5:0.5b` |
| `OLLAMA_REQUEST_TIMEOUT` | classify timeout | `5s` |
| `OLLAMA_RETRY_ATTEMPTS` | retries before fallback | `1` |

The total classification budget (HTTP + gRPC) is derived from `OLLAMA_REQUEST_TIMEOUT` × (retries + 1) plus margin, so slow local models are not cut off mid-retry.

## Development

```sh
make test    # unit tests
make vet     # static analysis
make fmt     # gofmt
make generate  # regenerate gRPC code (requires protoc + protoc-gen-go)
```

## Project layout

```
cmd/gateway/            entrypoint
internal/api/           HTTP + gRPC handlers, proto definitions
internal/breaker/       circuit breaker
internal/config/        YAML/.env/env config loading
internal/dashboard/     embedded web UI (embed.FS)
internal/intent/        Layer 1 classifier + heuristic fallback + quick replies
internal/metrics/       rolling-window metrics collector
internal/pool/          bounded worker pool
internal/sse/           SSE stream parser
internal/upstream/      Ollama chat client
```
