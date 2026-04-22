// engine_cpp/tests/unit/retriever_test.cc
//
// Phase 3b final slice — unit coverage for `aegis::rag::Retriever`.
//
// Test-first discipline (CLAUDE.md Rule 2, Incident 14 lesson): this
// file lands in the same commit as `retriever.{h,cc}`. If a future
// refactor violates the Retriever contract (non-monotonic hint_id,
// suggestion not from the top match, citations not populated), these
// assertions catch it before the ROADMAP's "hint panel actually fires
// on Taiwan corpus" demo regresses.
//
// Uses injected fakes (FakeEmbedder + FakeVectorSearcher) so the suite
// runs in plain `bazel test //engine_cpp/tests/unit:retriever_test`
// without requiring a live Qdrant or the 438 MB bge-m3 model. End-to-
// end coverage (real embedder + real Qdrant + Session stream) is the
// engine_seed integration test's territory.

#include "engine_cpp/src/rag/retriever.h"

#include <map>
#include <optional>
#include <string>
#include <string_view>
#include <utility>
#include <vector>

#include "absl/status/status.h"
#include "absl/status/statusor.h"
#include "absl/types/span.h"
#include "engine_cpp/src/inference/embedder.h"
#include "engine_cpp/src/vectordb/qdrant_client.h"
#include "gtest/gtest.h"

namespace aegis::rag {
namespace {

// Returns a fixed 4-dim vector (or a canned error).
class FakeEmbedder : public inference::Embedder {
public:
  absl::StatusOr<std::vector<float>> Embed(std::string_view text) override {
    last_input_ = std::string(text);
    if (fail_with_.has_value()) {
      return *fail_with_;
    }
    if (text.empty()) {
      return absl::InvalidArgumentError("FakeEmbedder: empty");
    }
    return vector_;
  }
  int Dimensions() const override { return static_cast<int>(vector_.size()); }
  std::string_view ModelTag() const override { return "fake/test/v1"; }

  std::vector<float> vector_ = {0.1f, 0.2f, 0.3f, 0.4f};
  std::optional<absl::Status> fail_with_;
  std::string last_input_;
};

class FakeVectorSearcher : public vectordb::VectorSearcher {
public:
  absl::StatusOr<std::vector<vectordb::SearchResult>>
  Search(std::string_view collection, absl::Span<const float> query_vec,
         int top_k) override {
    last_collection_ = std::string(collection);
    last_top_k_ = top_k;
    last_query_vec_.assign(query_vec.begin(), query_vec.end());
    if (fail_with_.has_value()) {
      return *fail_with_;
    }
    return results_;
  }

  std::vector<vectordb::SearchResult> results_;
  std::optional<absl::Status> fail_with_;
  std::string last_collection_;
  int last_top_k_ = 0;
  std::vector<float> last_query_vec_;
};

vectordb::SearchResult MakeResult(std::string id, float score, std::string text,
                                  std::string source_path,
                                  std::string chunk_index) {
  vectordb::SearchResult r;
  r.id = std::move(id);
  r.score = score;
  r.payload["text"] = std::move(text);
  r.payload["source_path"] = std::move(source_path);
  r.payload["chunk_index"] = std::move(chunk_index);
  return r;
}

TEST(RetrieverTest, BuildsHintFromTopMatchWithCitations) {
  FakeEmbedder e;
  FakeVectorSearcher s;
  s.results_ = {
      MakeResult("uuid-1", 0.921f, "Taiwan is an island in East Asia.",
                 "docs/rag/taiwan.md", "0"),
      MakeResult("uuid-2", 0.813f, "Its capital is Taipei.",
                 "docs/rag/taiwan.md", "1"),
  };

  Retriever r(&e, &s, "aegis_taiwan");
  auto hint_or = r.Retrieve("What is Taiwan?");
  ASSERT_TRUE(hint_or.ok()) << hint_or.status();

  EXPECT_EQ(hint_or->hint_id(), 1u);
  EXPECT_EQ(hint_or->suggestion(), "Taiwan is an island in East Asia.");
  EXPECT_EQ(hint_or->urgency(), aegis::v1::HINT_URGENCY_NORMAL);
  EXPECT_NE(hint_or->rationale().find("aegis_taiwan"), std::string::npos);
  EXPECT_NE(hint_or->rationale().find("0.921"), std::string::npos);

  ASSERT_EQ(hint_or->citations_size(), 2);
  EXPECT_EQ(hint_or->citations(0).doc_id(), "docs/rag/taiwan.md");
  EXPECT_EQ(hint_or->citations(0).quote(), "Taiwan is an island in East Asia.");
  EXPECT_EQ(hint_or->citations(0).location(), "0");
  EXPECT_EQ(hint_or->citations(1).location(), "1");

  // Searcher was called with the right collection, top_k, and the
  // embedder's output vector.
  EXPECT_EQ(s.last_collection_, "aegis_taiwan");
  EXPECT_EQ(s.last_top_k_, 3);
  ASSERT_EQ(s.last_query_vec_.size(), 4u);
  EXPECT_FLOAT_EQ(s.last_query_vec_[0], 0.1f);
  EXPECT_EQ(e.last_input_, "What is Taiwan?");
}

TEST(RetrieverTest, HintIdsAreMonotonicStartingAtOne) {
  FakeEmbedder e;
  FakeVectorSearcher s;
  Retriever r(&e, &s, "aegis_col");

  // Distinct point ids per call — otherwise the same-topic dedupe
  // (Retriever::last_top_point_id_) suppresses h2 and h3.
  s.results_ = {MakeResult("uuid-a", 0.9f, "ctx a", "doc.md", "0")};
  auto h1 = r.Retrieve("first");
  s.results_ = {MakeResult("uuid-b", 0.9f, "ctx b", "doc.md", "1")};
  auto h2 = r.Retrieve("second");
  s.results_ = {MakeResult("uuid-c", 0.9f, "ctx c", "doc.md", "2")};
  auto h3 = r.Retrieve("third");

  ASSERT_TRUE(h1.ok()) << h1.status();
  ASSERT_TRUE(h2.ok()) << h2.status();
  ASSERT_TRUE(h3.ok()) << h3.status();
  EXPECT_EQ(h1->hint_id(), 1u);
  EXPECT_EQ(h2->hint_id(), 2u);
  EXPECT_EQ(h3->hint_id(), 3u);
}

TEST(RetrieverTest, TopScoreBelowMinScoreReturnsNotFound) {
  FakeEmbedder e;
  FakeVectorSearcher s;
  // Default min_score is 0.42; 0.30 is below the gate.
  s.results_ = {MakeResult("uuid-low", 0.30f, "mediocre match", "doc.md", "0")};

  Retriever r(&e, &s, "aegis_col");
  auto h = r.Retrieve("ambient chatter that doesn't match the corpus");
  ASSERT_FALSE(h.ok());
  EXPECT_EQ(h.status().code(), absl::StatusCode::kNotFound);
  EXPECT_NE(h.status().message().find("min_score"), std::string::npos)
      << "NotFound message must mention min_score for operator debuggability";
}

TEST(RetrieverTest, MinScoreThresholdIsConfigurable) {
  FakeEmbedder e;
  FakeVectorSearcher s;
  s.results_ = {MakeResult("uuid-mid", 0.35f, "borderline", "doc.md", "0")};

  // Lower the gate to 0.30 — the same 0.35 result now passes.
  Retriever::Config cfg;
  cfg.min_score = 0.30f;
  Retriever r(&e, &s, "aegis_col", cfg);
  auto h = r.Retrieve("query");
  ASSERT_TRUE(h.ok()) << h.status();
  EXPECT_EQ(h->suggestion(), "borderline");
}

TEST(RetrieverTest, ConsecutiveSameTopPointIsDedupedToNotFound) {
  FakeEmbedder e;
  FakeVectorSearcher s;
  // Same top point id on both calls — simulates a speaker staying on
  // one topic across two flush windows.
  s.results_ = {
      MakeResult("uuid-same", 0.9f, "Taiwan climate is subtropical.", "doc.md",
                 "0"),
  };

  Retriever r(&e, &s, "aegis_col");
  auto h1 = r.Retrieve("what is the climate in Taiwan?");
  ASSERT_TRUE(h1.ok()) << h1.status();

  auto h2 = r.Retrieve("tell me about Taiwan's climate again");
  ASSERT_FALSE(h2.ok());
  EXPECT_EQ(h2.status().code(), absl::StatusCode::kNotFound);
  EXPECT_NE(h2.status().message().find("dedupe"), std::string::npos)
      << "NotFound message must name the dedupe condition";
}

TEST(RetrieverTest, TopicReturnSequenceABABaEmitsEachTime) {
  FakeEmbedder e;
  FakeVectorSearcher s;
  Retriever r(&e, &s, "aegis_col");

  auto result_a = MakeResult("uuid-A", 0.9f, "ctx A", "doc.md", "0");
  auto result_b = MakeResult("uuid-B", 0.9f, "ctx B", "doc.md", "1");

  // A → B → A should all emit — dedupe only suppresses consecutive
  // same-top, not the whole history.
  s.results_ = {result_a};
  auto h1 = r.Retrieve("first mention of A");
  ASSERT_TRUE(h1.ok()) << h1.status();
  EXPECT_EQ(h1->suggestion(), "ctx A");

  s.results_ = {result_b};
  auto h2 = r.Retrieve("pivot to B");
  ASSERT_TRUE(h2.ok()) << h2.status();
  EXPECT_EQ(h2->suggestion(), "ctx B");

  s.results_ = {result_a};
  auto h3 = r.Retrieve("return to A");
  ASSERT_TRUE(h3.ok()) << h3.status();
  EXPECT_EQ(h3->suggestion(), "ctx A");
}

TEST(RetrieverTest, EmptyTranscriptReturnsNotFound) {
  FakeEmbedder e;
  FakeVectorSearcher s;
  Retriever r(&e, &s, "aegis_col");
  auto h = r.Retrieve("");
  ASSERT_FALSE(h.ok());
  EXPECT_EQ(h.status().code(), absl::StatusCode::kNotFound);
  // Embedder was NOT called on empty input — contract is "skip early".
  EXPECT_TRUE(e.last_input_.empty());
}

TEST(RetrieverTest, PropagatesEmbedderError) {
  FakeEmbedder e;
  e.fail_with_ = absl::InternalError("FakeEmbedder: boom");
  FakeVectorSearcher s;
  Retriever r(&e, &s, "aegis_col");
  auto h = r.Retrieve("hello");
  ASSERT_FALSE(h.ok());
  EXPECT_EQ(h.status().code(), absl::StatusCode::kInternal);
}

TEST(RetrieverTest, PropagatesSearchError) {
  FakeEmbedder e;
  FakeVectorSearcher s;
  s.fail_with_ = absl::UnavailableError("qdrant down");
  Retriever r(&e, &s, "aegis_col");
  auto h = r.Retrieve("hello");
  ASSERT_FALSE(h.ok());
  EXPECT_EQ(h.status().code(), absl::StatusCode::kUnavailable);
}

TEST(RetrieverTest, EmptySearchResultsReturnsNotFound) {
  FakeEmbedder e;
  FakeVectorSearcher s;
  s.results_ = {}; // Qdrant returned nothing — no hint to emit.
  Retriever r(&e, &s, "aegis_col");
  auto h = r.Retrieve("hello");
  ASSERT_FALSE(h.ok());
  EXPECT_EQ(h.status().code(), absl::StatusCode::kNotFound);
}

TEST(RetrieverTest, CitationDocIdFallsBackToPointIdWhenNoSourcePath) {
  FakeEmbedder e;
  FakeVectorSearcher s;
  vectordb::SearchResult r1;
  r1.id = "just-an-id";
  r1.score = 0.5f;
  r1.payload["text"] = "some text";
  // No "source_path" payload — doc_id MUST fall back to point id.
  s.results_ = {r1};

  Retriever r(&e, &s, "aegis_col");
  auto h = r.Retrieve("hello");
  ASSERT_TRUE(h.ok()) << h.status();
  ASSERT_EQ(h->citations_size(), 1);
  EXPECT_EQ(h->citations(0).doc_id(), "just-an-id");
}

TEST(RetrieverTest, LongTextIsClippedAtUtf8BoundaryInSuggestion) {
  FakeEmbedder e;
  FakeVectorSearcher s;
  // Build a UTF-8 string where the byte-level cut at `excerpt_bytes`
  // would land mid-codepoint. Each "台" is 3 UTF-8 bytes. With default
  // excerpt_bytes=240, 80 "台" = 240 bytes exactly (boundary). We go
  // one "台" over to force clipping back to the boundary.
  std::string long_text;
  for (int i = 0; i < 81; ++i) {
    long_text += "台";
  }
  ASSERT_EQ(long_text.size(), 243u);

  s.results_ = {MakeResult("u", 0.9f, long_text, "d.md", "0")};

  Retriever r(&e, &s, "aegis_col");
  auto h = r.Retrieve("query");
  ASSERT_TRUE(h.ok()) << h.status();
  // Suggestion must be clipped at a codepoint boundary — length is a
  // multiple of 3 (UTF-8 width of "台"), NOT 240 which would split
  // the 81st "台" after its second byte.
  EXPECT_LE(h->suggestion().size(), 240u);
  EXPECT_EQ(h->suggestion().size() % 3, 0u)
      << "suggestion clipped mid-UTF-8-codepoint — ClipExcerpt did "
         "not walk back to the boundary";
}

TEST(RetrieverTest, LeadingMarkdownHeaderIsStrippedFromSuggestionAndCitations) {
  // Post-PR-#67 chunks begin with the section's `##` header line (e.g.
  // "## 氣候\n\n氣候方面，北回歸線貫穿全島..."). The hint UI renders
  // plain text, so the literal `##` is visual noise. Both suggestion
  // and citation quote must have the header prefix stripped — the
  // body content (starting with "氣候方面") should appear from char 0.
  FakeEmbedder e;
  FakeVectorSearcher s;
  s.results_ = {
      MakeResult("u", 0.9f,
                 "## 氣候\n\n氣候方面，北回歸線貫穿全島，氣候炎熱夏季偏長。",
                 "taiwan.md", "2"),
  };

  Retriever r(&e, &s, "aegis_taiwan");
  auto h = r.Retrieve("Taiwan weather");
  ASSERT_TRUE(h.ok()) << h.status();

  EXPECT_EQ(h->suggestion().find("##"), std::string::npos)
      << "suggestion still contains `##`: " << h->suggestion();
  EXPECT_EQ(h->suggestion().rfind("氣候", 0), 0u)
      << "suggestion does not begin with post-header content: "
      << h->suggestion();

  ASSERT_GT(h->citations_size(), 0);
  EXPECT_EQ(h->citations(0).quote().find("##"), std::string::npos)
      << "citation quote still contains `##`: " << h->citations(0).quote();
}

TEST(RetrieverTest, NonHeaderHashMarksAreNotStripped) {
  // In-text `#` without a trailing space (URL fragment, hashtag, code
  // sample, etc.) must NOT be treated as a markdown header. Only the
  // `^#+\s+` ATX-header pattern triggers stripping.
  FakeEmbedder e;
  FakeVectorSearcher s;
  s.results_ = {
      MakeResult("u", 0.9f, "#taiwan is a trending tag; see #foo/#bar.",
                 "doc.md", "0"),
  };

  Retriever r(&e, &s, "aegis_col");
  auto h = r.Retrieve("tag");
  ASSERT_TRUE(h.ok()) << h.status();
  EXPECT_EQ(h->suggestion().rfind("#taiwan", 0), 0u)
      << "hashtag at start was incorrectly stripped: " << h->suggestion();
}

} // namespace
} // namespace aegis::rag
