// engine_cpp/src/inference/ggml_embedder.h
//
// Default Embedder implementation per ADR-0020 §Decision.7.
// Loads a GGUF embedding model (bge-m3 Q4_K_M by default) via
// llama.cpp's C API and produces unit-normalized dense vectors.
//
// Thread safety: Embed() serializes on an internal mutex because
// ggml's forward pass context is not reentrant (ADR-0020 embedder.h
// contract). Concurrent Embed() calls from different sessions
// queue on the mutex; throughput scales with EmbedBatch() which
// processes multiple texts in a single locked forward pass.
//
// Lifecycle: one GGMLEmbedder per engine process, created at
// startup and registered with ModelBudget. Destroyed at shutdown.

#ifndef AEGIS_ENGINE_CPP_SRC_INFERENCE_GGML_EMBEDDER_H_
#define AEGIS_ENGINE_CPP_SRC_INFERENCE_GGML_EMBEDDER_H_

#include <memory>
#include <mutex>
#include <string>
#include <string_view>
#include <vector>

#include "absl/status/statusor.h"
#include "engine_cpp/src/inference/embedder.h"

// Forward declarations — avoid exposing llama.h in our public header.
struct llama_model;
struct llama_context;

namespace aegis::inference {

class GGMLEmbedder final : public Embedder {
public:
  // Factory. Loads the GGUF model from `model_path`. Returns
  // Internal error if loading fails.
  static absl::StatusOr<std::unique_ptr<GGMLEmbedder>>
  Create(const std::string &model_path);

  ~GGMLEmbedder() override;

  absl::StatusOr<std::vector<float>> Embed(std::string_view text) override;
  int Dimensions() const override;
  std::string_view ModelTag() const override;

private:
  GGMLEmbedder(llama_model *model, llama_context *ctx, int dims,
               std::string tag);

  llama_model *model_;
  llama_context *ctx_;
  int dims_;
  std::string tag_;
  std::mutex mu_;
};

} // namespace aegis::inference

#endif // AEGIS_ENGINE_CPP_SRC_INFERENCE_GGML_EMBEDDER_H_
