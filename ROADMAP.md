# 🗺️ Aegis Prompter Cloud (V2) - Roadmap

**Current Status:** Bootstrapping Project Architecture
**Last Updated By:** AI Architect Agent Initialization

## Phase 1: The Bazel Monorepo Engine 
- [ ] Initialize Bazel `WORKSPACE` and root rules.
- [ ] Create `proto/aegis.proto` and define the gRPC contracts (StreamTranscribe, AskRAG).
- [ ] Establish `engine_cpp/`: Integrate `whisper.cpp` basics wrapped in a native gRPC server.
- [ ] Establish `gateway_go/`: Setup the Go HTTP/2 gRPC server acting as a passthrough.

## Phase 2: Internal MVP & The BFF
- [ ] Go GW: Implement `gRPC-Web` multiplexing.
- [ ] Go GW: Implement Pion WebRTC to handle incoming browser UDP frames.
- [ ] Testing: Send raw audio files through the Go GW and verify C++ transcriptions return successfully.

## Phase 3: The Frontlines (Tauri / React)
- [ ] Scaffold `/frontend_web` using React + Vite.
- [ ] Implement `gRPC-Web` client generation via protobuf JS compiler.
- [ ] Implement local WebRTC media ingestion (`getUserMedia`).
- [ ] Tauri Shell: Wrap frontend into a lightweight Rust app to securely access CoreAudio (bypassing strict browser loopback limitations).

## Phase 4: SRE & Cloud Orchestration (EKS)
- [ ] Bazel `rules_oci`: Automate packaging of C++ and Go binaries into Distroless Docker containers.
- [ ] ECR Pipeline: CI/CD Github Actions integration.
- [ ] EKS YAML Specs: Define Services, Deployments, and GPU node affinities for C++ pods.
