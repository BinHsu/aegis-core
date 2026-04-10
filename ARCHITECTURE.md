# 🛡️ Aegis Core (V2 Enterprise)

## System Architecture Blueprint

This document defines the core architecture, technical stack, and design decisions for the Aegis Core (V2).
**ALL future AI agents must consult and update this document when making systemic changes.**

### 1. System Topology & Tech Stack
The architecture is designed as a **Microservice + BFF (Backend For Frontend)** topology, optimized for EKS deployment and ultra-low latency.

*   **Core Engine (C++)**: 
    - pure backend service handling inference.
    - Uses `whisper.cpp` / ML accelerators (Metal/CUDA).
    - Exposes a raw native **gRPC** server.
*   **API Gateway / BFF (Go)**:
    - Built with Go. 
    - Handles external WebRTC (via `Pion`) and terminates network logic.
    - Translates gRPC-Web from frontend into native gRPC for C++. 
*   **Web Client / App (React + Tauri)**:
    - Frontend built with React/Svelte.
    - Uses `gRPC-Web` to communicate with the Go GW strong-typed APIs.
    - Deployed as static assets (CloudFront/S3) OR packaged as a lightweight Mac/Win desktop app via **Tauri (Rust)** to access native OS microphones/audio out.
*   **Build System (Bazel Monorepo)**:
    - Entire project exists in a single Bazel Monorepo.
    - `.proto` files are stored at the root, generating strong-typed SDKs for C++, Go, and TS simultaneously.

### 2. Microservice Boundaries (EKS Deployment)
*   **Compute Pods (Node Affinities)**: C++ engine runs on hardware-accelerated nodes (e.g., AWS g4dn for Nvidia, or Apple Silicon local equivalence).
*   **Gateway Pods**: Lightweight Go pods handling I/O multiplexing.
*   **Multi-Tenancy**: Data separation via DynamoDB; physical compute separation for VIP clients via Fargate/Dedicated Instances.

### 3. SRE / FinOps Capacity Management (Multi-Tenancy)
The Go GW acts as a "Fleet Manager" routing tenants to their respective C++ engine pods based on tier constraints:
*   **Tier 3 (Shared/Economy)**: Uses a pre-warmed pool of C++ Pods running 24/7 on shared nodes. Scales via K8s HPA to prevent cold starts.
*   **Tier 2 (Dedicated Pod)**: Tenant receives an isolated Pod on a shared Node. Scales-to-zero when unused. Boots on-demand via UI provisioning delay ("Warming up engine...").
*   **Tier 1 (VIP/Enterprise)**: Strictly hardware-isolated (Node Affinity or AWS Fargate). Bootstrapped via Event-Driven architectures (e.g., calendar integrations triggering capacity warmup 15 minutes prior to the event).

### 4. Data Flow (Audio Pipeline)
1. Staff opens Tauri App (or Web Browser) and grabs Microphone (via WebAPI or OS CoreAudio).
2. Audio stream is sent via **WebRTC** to the Go Gateway.
3. Go GW unwraps the WebRTC UDP packets into raw PCM bytes.
4. Go GW pushes raw PCM bytes over a **Bidirectional gRPC Stream** to the C++ Engine.
5. C++ Engine transcribes and performs **Speaker Diarization**. Because we capture a single mixed audio track (simplifying hardware), the AI isolates speakers (e.g., Speaker_0, Speaker_1).
6. **VIP Identification**: The system maps an anonymous speaker to "The Boss" using either:
    - *Voice Enrollment*: Pre-meeting voiceprint cosine similarity matching.
    - *Human-in-the-loop*: The Staff manually tags a speaker ID from the Frontend UI.
7. C++ Engine streams standard Protobuf answers back to Go GW, which pushes to the Client UI via gRPC-Web or WebSockets.

### 5. Dual-Mode Parity (Local Monolith vs. Cloud Microservices)
To satisfy both "beginner-friendly local execution" and "EKS Cloud Deployment", the architecture enforces strict **Ports and Adapters (Hexagonal Architecture)** within the Go Gateway:
*   **Infrastructure Interfaces**: All external states (DB, Storage) are abstracted. 
    - `DeployMode=LOCAL`: Uses SQLite (or In-Memory) and Local File Storage.
    - `DeployMode=CLOUD`: Injects DynamoDB and AWS S3 AWS SDK adapters.
*   **Process Supervisor Pattern**: 
    - In `CLOUD` mode, Go GW and C++ Engine run in separate K8s Pods communicating via Istio/Envoy.
    - In `LOCAL` mode, a user simply runs `bazel run //:app_local`. The Go GW acts as a local supervisor, programmatically spinning up the C++ binary as a child background process (`exec.Command`) to simulate the microservice network internally, without requiring the user to open multiple terminals.

### 6. AI Models & Hardware Resource Optimization
The system targets an absolute physical ceiling of **16GB Unified Memory** (e.g., MacBook Air M4 base-high tier) to guarantee successful `LOCAL` mode deployments without crashing.
*   **Engine & Model Quantization (The < 8GB Budget)**:
    - **Transcription**: `whisper.cpp` using `large-v3-turbo` (4-bit Q4 quantization). Cost: ~1.5GB
    - **Diarization**: Lightweight Voice embedding clustering (e.g., pyannote/speaker-diarization). Cost: ~1.0GB
    - **Embeddings**: `sentence-transformers.cpp` for multilingual text. Cost: ~0.5GB
    - **Inference (Optional LLM)**: `llama.cpp` using Llama-3-8B-Instruct (4-bit Q4_K_M). Cost: ~4.8GB
*   **Dual-Mode RAG Mounting Strategy**:
    - `LOCAL` Mode: Uses **In-Process Vector Database**. C++ engine mmaps a precompiled `.bin` vector index locally and searches via `hnswlib` in-memory (< 5ms latency, 0 external networking).
    - `CLOUD` Mode: Uses **External Enterprise Vector DB** (e.g., Qdrant, Milvus, AWS OpenSearch). The C++ pod converts text to embedding vector, then shoots a gRPC request to the Vector DB clustered backend allowing dynamic hot-reloads of knowledge bases across multiple active Pods.

### 7. Cross-Repository Cloud Infrastructure (DevOps Boundary)
This repository (`aegis-core`) is an Application Monorepo. However, it relies on a separately maintained Infrastructure as Code (IaC) repository for underlying AWS network orchestration.
*   **Infra Repository**: [aegis-aws-landing-zone](https://github.com/BinHsu/aegis-aws-landing-zone)
*   **Boundary Rules**:
    - **App Repo (Here / The Payload)**: Holds ALL application logic, Bazel build rules, Github Actions CI code, and Kubernetes Deployment/Service/Helm YAMLs. The App dictates how it wants to run on K8s (e.g., replica count, resource limits).
    - **Infra Repo (There / The Pointer)**: Holds Terraform/CDK to spin up the actual AWS EKS Clusters, DynamoDB Tables, S3 Buckets, and cross-account IAM OIDC identity providers. It also hosts the ArgoCD *configuration* (the `Application` CRD) that simply points ArgoCD to watch this App Repo.
*   **GitOps Conflict Prevention (Zero Overlap)**:
    - ArgoCD Server runs in the Infra repo's EKS cluster, but it polls the K8s manifests located in THIS Application repository.
    - Application engineers push K8s YAML changes here. ArgoCD detects the change and automatically syncs the EKS cluster.
    - *Note to future Agents: Do not try to insert Terraform code to build an EKS cluster here. Direct the user to the landing-zone repository.*

### 8. Enterprise Standards (Security & Observability)
To pass strict compliance and enterprise security audits, this application enforces the following patterns. (The infrastructure for these is provisioned in the landing-zone repository).
*   **Zero Trust Networking (mTLS)**: Even within the EKS cluster, the communication between the Go Gateway and the C++ Engine is protected by a Service Mesh (e.g., Istio) enforcing Mutual TLS.
*   **Identity First (EKS Pod Identity & Cognito)**: 
    - *Server-side*: No hardcoded AWS credentials or IAM static keys exist. The Go Gateway authenticates to DynamoDB/S3 using **EKS Pod Identity**, transparently assuming IAM roles. 
    - *Client-side*: End users log into the Tauri/React app via **AWS Cognito**, passing JWT tokens down to the Go Gateway for validation.
    - *Secrets*: Any unavoidable non-IAM secrets (e.g. 3rd-party API keys) are injected dynamically via AWS Secrets Manager using the External Secrets Operator. Rotation must be elegantly handled at the K8s object level.
*   **Distributed Tracing (OpenTelemetry)**: The Go BFF MUST inject an OpenTelemetry `TraceID` upon receiving the WebRTC stream. This context must be propagated out-of-band via gRPC metadata to the C++ Engine to provide full waterfall latency visibility (WebRTC -> Go -> C++ Whisper -> Vector DB).
*   **The "Local Mode" Interface Fallback (Crucial!)**: 
    - Because Aegis V2 must still run strictly offline on a portable SSD (`DeployMode=LOCAL`), **all enterprise components above MUST be abstracted behind Interfaces.**
    - *Auth Fallback*: When `DeployMode=LOCAL`, the Cognito JWT middleware is bypassed or replaced with a dummy local token authenticator.
    - *Secrets Fallback*: The External Secrets Operator logic gracefully falls back to reading a local `.env` file within the Bazel sandbox.
    - *Telemetry Fallback*: OpenTelemetry spans are exported to `stdout` (Console) instead of an AWS X-Ray/Tempo collector.
