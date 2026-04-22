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

// Strip a leading markdown ATX header (`^#+\s+`) from the chunk
// text. Since PR #67 the chunker treats `##` lines as hard segment
// boundaries, so each section's first chunk begins with the header
// line itself ("## 氣候\n\n氣候方面，北回歸線..."). The hint panel
// renders plain text, so the literal `##` reads as visual noise.
// Stripping it preserves the semantic anchor (the header WORD) —
// only the `#` characters and the single whitespace run immediately
// after them are removed.
//
// Requires at least one space/tab after the `#` run to qualify as
// a header, so an in-text fragment like `#taiwan` or URL hash is
// left untouched. UTF-8 safe — CJK bytes are never in 0x20-0x23
// range.
std::string_view StripLeadingMarkdownHeader(std::string_view text) {
  std::size_t pos = 0;
  while (pos < text.size() && text[pos] == '#') {
    ++pos;
  }
  if (pos == 0) {
    return text;
  }
  // Must be followed by whitespace to qualify as an ATX header.
  if (pos >= text.size() || (text[pos] != ' ' && text[pos] != '\t')) {
    return text;
  }
  while (pos < text.size() && (text[pos] == ' ' || text[pos] == '\t')) {
    ++pos;
  }
  return text.substr(pos);
}

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

  const auto &top = results->front();

  // Score gate — Qdrant top-K always returns K results if the
  // collection has ≥ K points, regardless of relevance. Without
  // this check, every filler utterance ("um", "let me think") fires
  // a hint with the nearest-but-irrelevant chunk. See Config::min_score.
  if (top.score < config_.min_score) {
    return absl::NotFoundError(
        absl::StrFormat("Retriever: top score %.3f below min_score %.3f",
                        top.score, config_.min_score));
  }

  // Consecutive-same-topic dedupe. When a speaker stays on one topic
  // across multiple 3 s flush windows the top match is usually the
  // same chunk; re-emitting it every window spams the viewer with
  // an identical suggestion. Suppress when the top point id matches
  // the last emission; let A → B → A through (intentional topic
  // return). Empty last_top_point_id_ (first call) always passes.
  if (top.id == last_top_point_id_) {
    return absl::NotFoundError(
        absl::StrCat("Retriever: top match '", top.id,
                     "' matches last emission (same-topic dedupe)"));
  }
  last_top_point_id_ = top.id;

  aegis::v1::PrompterHint hint;
  hint.set_hint_id(next_hint_id_++);

  const std::string top_text = LookupPayload(top.payload, "text");
  hint.set_suggestion(
      ClipExcerpt(StripLeadingMarkdownHeader(top_text), config_.excerpt_bytes));
  hint.set_rationale(absl::StrFormat("Related context from '%s' (score=%.3f)",
                                     collection_, top.score));
  hint.set_urgency(aegis::v1::HINT_URGENCY_NORMAL);

  for (const auto &r : *results) {
    auto *cit = hint.add_citations();
    const std::string src = LookupPayload(r.payload, "source_path");
    cit->set_doc_id(src.empty() ? r.id : src);
    cit->set_quote(ClipExcerpt(
        StripLeadingMarkdownHeader(LookupPayload(r.payload, "text")),
        config_.excerpt_bytes));
    cit->set_location(LookupPayload(r.payload, "chunk_index"));
  }
  return hint;
}

} // namespace aegis::rag
