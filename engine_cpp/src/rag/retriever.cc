// engine_cpp/src/rag/retriever.cc

#include "engine_cpp/src/rag/retriever.h"

#include <string>
#include <string_view>
#include <utility>
#include <vector>

#include "absl/status/status.h"
#include "absl/strings/str_cat.h"
#include "absl/strings/str_format.h"
#include "absl/types/span.h"

namespace aegis::rag {

namespace {

// Byte-level clip that walks back to a UTF-8 codepoint boundary so we
// never leave a half-encoded character in the UI. UTF-8 continuation
// bytes match `10xxxxxx` (0x80–0xBF); lead bytes are anything else.
std::string ClipExcerpt(std::string_view text, std::size_t max_bytes) {
  if (text.size() <= max_bytes) {
    return std::string(text);
  }
  std::size_t cut = max_bytes;
  while (cut > 0 && (static_cast<unsigned char>(text[cut]) & 0xC0) == 0x80) {
    --cut;
  }
  return std::string(text.substr(0, cut));
}

std::string LookupPayload(const std::map<std::string, std::string> &payload,
                          const char *key) {
  auto it = payload.find(key);
  return it == payload.end() ? std::string() : it->second;
}

} // namespace

Retriever::Retriever(inference::Embedder *embedder,
                     vectordb::VectorSearcher *searcher,
                     std::string collection) noexcept
    : Retriever(embedder, searcher, std::move(collection), Config{}) {}

Retriever::Retriever(inference::Embedder *embedder,
                     vectordb::VectorSearcher *searcher, std::string collection,
                     Config config) noexcept
    : embedder_(embedder), searcher_(searcher),
      collection_(std::move(collection)), config_(config) {}

absl::StatusOr<aegis::v1::PrompterHint>
Retriever::Retrieve(std::string_view transcript_text) {
  if (transcript_text.empty()) {
    return absl::NotFoundError("Retriever: empty transcript; no hint to emit");
  }

  auto vec = embedder_->Embed(transcript_text);
  if (!vec.ok()) {
    return vec.status();
  }
  if (vec->empty()) {
    return absl::InvalidArgumentError(
        "Retriever: embedder returned empty vector");
  }

  auto results =
      searcher_->Search(collection_, absl::MakeConstSpan(*vec), config_.top_k);
  if (!results.ok()) {
    return results.status();
  }
  if (results->empty()) {
    return absl::NotFoundError(
        absl::StrCat("Retriever: no matches in '", collection_, "'"));
  }

  aegis::v1::PrompterHint hint;
  hint.set_hint_id(next_hint_id_++);

  const auto &top = results->front();
  const std::string top_text = LookupPayload(top.payload, "text");
  hint.set_suggestion(ClipExcerpt(top_text, config_.excerpt_bytes));
  hint.set_rationale(absl::StrFormat("Related context from '%s' (score=%.3f)",
                                     collection_, top.score));
  hint.set_urgency(aegis::v1::HINT_URGENCY_NORMAL);

  for (const auto &r : *results) {
    auto *cit = hint.add_citations();
    const std::string src = LookupPayload(r.payload, "source_path");
    cit->set_doc_id(src.empty() ? r.id : src);
    cit->set_quote(
        ClipExcerpt(LookupPayload(r.payload, "text"), config_.excerpt_bytes));
    cit->set_location(LookupPayload(r.payload, "chunk_index"));
  }
  return hint;
}

} // namespace aegis::rag
