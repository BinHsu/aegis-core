// engine_cpp/src/inference/embedder.cc

#include "engine_cpp/src/inference/embedder.h"

#include <string_view>
#include <utility>
#include <vector>

#include "absl/status/statusor.h"
#include "absl/types/span.h"

namespace aegis::inference {

absl::StatusOr<std::vector<std::vector<float>>>
Embedder::EmbedBatch(absl::Span<const std::string_view> texts) {
  std::vector<std::vector<float>> out;
  out.reserve(texts.size());
  for (const std::string_view text : texts) {
    auto r = Embed(text);
    if (!r.ok()) {
      return r.status();
    }
    out.push_back(std::move(*r));
  }
  return out;
}

} // namespace aegis::inference
