# 🛡️ Aegis Core (V2)

> **"Turn every remote meeting into a strategic advantage."**
> *(一個設計給大型企業與公關團隊的「防社死提詞機」：第二代雲端原生架構)*

Welcome to Aegis Core! If you are looking for the original lightweight Python/Mac-only setup, check out the [V1 Repository](https://github.com/BinHsu/Aegis-Prompter).

This V2 project is the enterprise evolution. We rewrote the core in C++ for maximum power, added a Go server to handle network translation, and used Tauri (Rust) for a sleek frontend. It is orchestrated via a Bazel Monorepo, meaning you can still compile and run this complex beast on your local machine with minimal effort!

## 📖 For Beginners / "My Mom" / Friendly AIs
Do not be intimidated by the terms C++, Go, or Bazel. 
Our primary ethos remains: **"Clone it, build it, and it just works locally."**
Unlike V1 (which was bound to Apple Silicon Macs), V2 is fully **Cross-Platform**. Thanks to `whisper.cpp` and WebRTC, you can run this locally on macOS (Metal), Windows (CUDA), or Linux (CUDA/AVX2).

You do not need to install heavy IDEs or configure crazy paths. Once development moves into Phase 3, we will provide a single script (like `bazel run //...`) that downloads everything required, compiles the entire microservice stack on your laptop, and launches the user interface.

## 🏗️ Architecture at a Glance
This system uses a **BFF (Backend For Frontend)** Microservice architecture.
1. **Frontend (Tauri / Web)**: You speak into the microphone. It securely sends audio via WebRTC.
2. **Go Gateway (BFF)**: Receives the complicated WebRTC audio and simplifies it into gRPC data chunks.
3. **C++ Engine**: The beast inside. It eats audio, uses AI to create text, figures out what you should say next via local RAG, and shoots the tactical hint back out.

## 🤝 For AI Developers & Contributors
Before you edit any code, you MUST:
1. Read `CLAUDE.md` for our strict collaboration rules.
2. Check `ARCHITECTURE.md` to understand where things belong.
3. Update `ROADMAP.md` when you finish a task.

We use **Bazel** to guarantee isolated, reproducible builds. Do not break the compilation boundaries!
