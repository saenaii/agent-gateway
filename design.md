# Role and Target
You are a Senior Systems Architect & Go Performance Engineer.
Your task is to design and implement a high-performance, production-ready LLM Gateway in Go. The gateway acts as a smart semantic router and observability proxy between clients and LLM providers.

# Architecture & Engineering Principles
- **Language & Runtime**: Go (latest stable), structured using standard Go project layout (`/cmd`, `/internal`, `/pkg`, `/config`).
- **Protocols & Endpoints**:
  - Expose OpenAI-compatible HTTP REST endpoints (`POST /v1/chat/completions`) supporting both standard JSON and Server-Sent Events (SSE) streaming.
  - Expose a gRPC endpoint for high-throughput internal microservice calls.
  - Expose an HTTP `/dashboard` endpoint serving the embedded web UI.
- **2-Layer Semantic Routing Pipeline**:
  - **Layer 1 (Intent Classifier)**: Route user prompt to a local small LLM (e.g., Qwen2.5-0.5B / Ollama in Docker). The local LLM MUST be instructed to return a structured JSON response categorizing the query (e.g., `INTENT_GREETING`, `INTENT_TRIVIAL`, or `INTENT_COMPLEX`).
    - If `GREETING` or `TRIVIAL`: Layer 1 returns the quick response directly (0 online token cost).
    - If `COMPLEX`: Escalate to Layer 2.
  - **Layer 2 (Commercial/Online LLM)**: Call configured upstream models (e.g., DeepSeek / Claude / OpenAI) with Tool Calling support.
- **Dynamic Configuration & Extensions**:
  - Configuration via `config.yaml` with environment variable overrides.
  - **MCP (Model Context Protocol) & Skills**: Dynamically load MCP servers/tools and skill definitions on startup, automatically mapping MCP tools to OpenAI Tool Specifications for Layer 2.
  - Git hygiene: Include `config.example.yaml` in the repo and add `config.yaml` to `.gitignore`.

# Technical Specifications
- **Concurrency & Resiliency**:
  - Worker pool and context deadline handling for upstream HTTP requests.
  - Circuit breaker and retry logic for Layer 2 upstream model calls.
- **Streaming Proxying (SSE)**:
  - Zero-copy or minimal-allocation streaming proxy for SSE responses.
  - Real-time SSE parser to extract `usage` token metrics without blocking the stream.
- **Observability Metrics Collection**:
  - In-memory thread-safe metrics collector with rolling window calculation.
  - Track: Total Token Spend (Prompt/Completion), Layer 1 Offload Ratio, Layer 2 Escalation Ratio, Latency Metrics (TTFT, Total Duration), and Token Cost percentiles (P50, P90, P99).

# Dashboard Specifications
- **UI Architecture**: Embedded Single-Page Application (HTML/JS using Go `embed.FS` with Tailwind CSS + Chart.js via CDN). No node_modules build step required for simple deployment.
- **Key Visualizations**:
  - **Traffic Split**: Pie/Doughnut chart showing Layer 1 (Local Offload) vs Layer 2 (Online High-Capacity) request distribution.
  - **Token Usage**: Real-time line chart for Token consumption over time.
  - **Performance Summary**: Gauge/Stat Cards for Avg/Max/P99 Token Cost and TTFT (Time to First Token).
  - **Live Request Log**: Real-time stream/table of incoming requests showing assigned Layer, Intent Status, TTFT, and Token Count.

# Code Generation Requirements
1. Provide fully runnable, idiomatic Go code without omitted placeholders in critical logic.
2. Include a clean `docker-compose.yml` that provisions:
   - The Go Gateway Service.
   - Ollama / vLLM container for Layer 1 local inference (pre-configured to pull `qwen2.5:0.5b`).
3. Include a comprehensive `Makefile` with targets for `build`, `run`, `test`, and `docker-up`.
