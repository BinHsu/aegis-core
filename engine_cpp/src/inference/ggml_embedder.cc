// engine_cpp/src/inference/ggml_embedder.cc

#include "engine_cpp/src/inference/ggml_embedder.h"

#include <cmath>
#include <cstdint>
#include <memory>
#include <mutex>
#include <string>
#include <string_view>
#include <vector>

#include "absl/status/status.h"
#include "absl/status/statusor.h"
#include "absl/strings/str_cat.h"
#include "llama.h"

namespace aegis::inference {

namespace {

// L2-normalize a vector in place. llama.cpp's embeddings are NOT
// pre-normalized — the caller must normalize before cosine similarity.
void L2Normalize(std::vector<float> &vec) {
  float sum_sq = 0.0f;
  for (const float v : vec) {
    sum_sq += v * v;
  }
  if (sum_sq > 0.0f) {
    const float inv_norm = 1.0f / std::sqrt(sum_sq);
    for (float &v : vec) {
      v *= inv_norm;
    }
  }
}

} // namespace

absl::StatusOr<std::unique_ptr<GGMLEmbedder>>
GGMLEmbedder::Create(const std::string &model_path) {
  // Initialize llama backend (safe to call multiple times).
  llama_backend_init();

  // Load model.
  llama_model_params model_params = llama_model_default_params();
  llama_model *model =
      llama_model_load_from_file(model_path.c_str(), model_params);
  if (model == nullptr) {
    return absl::InternalError(
        absl::StrCat("GGMLEmbedder: failed to load model: ", model_path));
  }

  // Create context with embedding mode enabled.
  llama_context_params ctx_params = llama_context_default_params();
  ctx_params.n_ctx = 512; // bge-m3 max context
  ctx_params.n_batch = 512;
  ctx_params.embeddings = true;
  ctx_params.n_threads = 4;

  llama_context *ctx = llama_init_from_model(model, ctx_params);
  if (ctx == nullptr) {
    llama_model_free(model);
    return absl::InternalError("GGMLEmbedder: failed to create llama context");
  }

  const int dims = llama_model_n_embd(model);
  if (dims <= 0) {
    llama_free(ctx);
    llama_model_free(model);
    return absl::InternalError(
        absl::StrCat("GGMLEmbedder: invalid embedding dimensions: ", dims));
  }

  // Build model tag from metadata.
  std::string tag = absl::StrCat("ggml/", model_path, "/", dims, "d");

  return std::unique_ptr<GGMLEmbedder>(
      new GGMLEmbedder(model, ctx, dims, std::move(tag)));
}

GGMLEmbedder::GGMLEmbedder(llama_model *model, llama_context *ctx, int dims,
                           std::string tag)
    : model_(model), ctx_(ctx), dims_(dims), tag_(std::move(tag)) {}

GGMLEmbedder::~GGMLEmbedder() {
  if (ctx_ != nullptr) {
    llama_free(ctx_);
  }
  if (model_ != nullptr) {
    llama_model_free(model_);
  }
}

absl::StatusOr<std::vector<float>> GGMLEmbedder::Embed(std::string_view text) {
  if (text.empty()) {
    return absl::InvalidArgumentError("GGMLEmbedder: empty text");
  }

  std::lock_guard<std::mutex> lock(mu_);

  // Tokenize via the model's vocab. bge-m3 context is 512 tokens max.
  const std::string text_str(text);
  const llama_vocab *vocab = llama_model_get_vocab(model_);
  std::vector<llama_token> tokens(512);
  const int n_tokens = llama_tokenize(
      vocab, text_str.c_str(), static_cast<int32_t>(text_str.size()),
      tokens.data(), static_cast<int32_t>(tokens.size()),
      /*add_special=*/true, /*parse_special=*/false);

  if (n_tokens < 0) {
    return absl::InternalError(
        absl::StrCat("GGMLEmbedder: tokenization failed, code=", n_tokens));
  }
  tokens.resize(n_tokens);

  // Build batch.
  llama_batch batch = llama_batch_init(n_tokens, 0, 1);
  for (int i = 0; i < n_tokens; ++i) {
    batch.token[i] = tokens[i];
    batch.pos[i] = i;
    batch.n_seq_id[i] = 1;
    batch.seq_id[i][0] = 0;
    batch.logits[i] = (i == n_tokens - 1) ? 1 : 0;
  }
  batch.n_tokens = n_tokens;

  // Encode (not decode — encoder-only models require llama_encode).
  const int rc = llama_encode(ctx_, batch);
  llama_batch_free(batch);

  if (rc != 0) {
    return absl::InternalError(
        absl::StrCat("GGMLEmbedder: llama_encode failed, code=", rc));
  }

  // Extract embedding. For BERT/bge-m3, use sequence-level embedding
  // (CLS pooling or mean pooling depending on model metadata).
  const float *embd = llama_get_embeddings_seq(ctx_, 0);
  if (embd == nullptr) {
    // Fallback: try token-level embedding for the last token.
    embd = llama_get_embeddings_ith(ctx_, n_tokens - 1);
  }
  if (embd == nullptr) {
    return absl::InternalError("GGMLEmbedder: failed to extract embeddings");
  }

  std::vector<float> result(embd, embd + dims_);
  L2Normalize(result);
  return result;
}

int GGMLEmbedder::Dimensions() const { return dims_; }

std::string_view GGMLEmbedder::ModelTag() const { return tag_; }

} // namespace aegis::inference
