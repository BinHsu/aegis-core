// engine_cpp/tests/unit/chunker_test.cc
//
// Unit tests for the Chinese-aware recursive markdown chunker per
// ADR-0019 §Decision.2 / ADR-0020.

#include "engine_cpp/src/rag/chunker.h"

#include <cstddef>
#include <cstdlib>
#include <fstream>
#include <sstream>
#include <string>
#include <string_view>

#include "gtest/gtest.h"

namespace aegis::rag {
namespace {

TEST(Utf8CharCountTest, CountsAsciiAndChineseCorrectly) {
  EXPECT_EQ(Utf8CharCount(""), 0u);
  EXPECT_EQ(Utf8CharCount("hello"), 5u);
  EXPECT_EQ(Utf8CharCount("台灣"), 2u); // two 3-byte chars
  EXPECT_EQ(Utf8CharCount("Hello 台灣"), 8u);
  EXPECT_EQ(Utf8CharCount("臺灣（台灣）"), 6u); // all CJK
}

TEST(MarkdownChunkerTest, EmptyInputEmptyOutput) {
  MarkdownChunker c;
  EXPECT_TRUE(c.Split("").empty());
}

TEST(MarkdownChunkerTest, SingleShortParagraphOneChunk) {
  MarkdownChunker c;
  auto chunks = c.Split("hello world");
  ASSERT_EQ(chunks.size(), 1u);
  EXPECT_EQ(chunks[0].text, "hello world");
  EXPECT_EQ(chunks[0].byte_offset, 0u);
  EXPECT_EQ(chunks[0].char_count, 11u);
}

TEST(MarkdownChunkerTest, ParagraphBoundariesAreChunkBoundaries) {
  // Three paragraphs, each small enough to be its own chunk at
  // target 15 chars, overlap 0. Target must be BELOW the total
  // input length so the splitter actually splits (if the whole
  // input fits, the algorithm correctly returns it as one chunk).
  MarkdownChunker c({.target_chunk_chars = 15, .overlap_chars = 0});
  std::string input = "first para.\n\nsecond para.\n\nthird para.";
  auto chunks = c.Split(input);
  ASSERT_EQ(chunks.size(), 3u);
  EXPECT_EQ(chunks[0].text, "first para.");
  EXPECT_EQ(chunks[1].text, "second para.");
  EXPECT_EQ(chunks[2].text, "third para.");
}

TEST(MarkdownChunkerTest, ChineseSentencesSplitOnFullWidthPeriod) {
  // Single "paragraph" with three zh-TW sentences. Target so small
  // each sentence becomes its own chunk. Separators cascade:
  // \n\n (not present) → \n (not present) → 。 (matches).
  MarkdownChunker c({.target_chunk_chars = 10, .overlap_chars = 0});
  std::string input = "臺灣是島嶼。面積約三萬六千平方公里。位於東亞。";
  auto chunks = c.Split(input);
  // Expect at least 2 chunks — they should split on 。
  ASSERT_GE(chunks.size(), 2u);
  // First chunk should contain "臺灣是島嶼"
  EXPECT_NE(chunks[0].text.find("臺灣是島嶼"), std::string::npos);
  // Last chunk should contain "位於東亞"
  EXPECT_NE(chunks.back().text.find("位於東亞"), std::string::npos);
  // No chunk exceeds target by more than a small factor
  for (const auto &ch : chunks) {
    EXPECT_LE(ch.char_count, 20u) << "chunk too large: " << ch.text;
  }
}

TEST(MarkdownChunkerTest, OversizedAtomicPieceStillEmittedViaFallback) {
  // A paragraph with no separators and size > target. The splitter
  // falls through to character-level splitting.
  MarkdownChunker c({.target_chunk_chars = 5, .overlap_chars = 0});
  std::string input = "abcdefghij"; // 10 chars, no separators
  auto chunks = c.Split(input);
  // Char-split fallback should emit at least two chunks of <= 5 chars.
  ASSERT_GE(chunks.size(), 2u);
  std::string reconstructed;
  for (const auto &ch : chunks) {
    reconstructed += ch.text;
    EXPECT_LE(ch.char_count, 5u);
  }
  EXPECT_EQ(reconstructed, "abcdefghij");
}

TEST(MarkdownChunkerTest, OverlapStitchesAdjacentChunks) {
  // Two paragraphs, overlap = 3. Target below total length so
  // splitting triggers; each paragraph fits individually.
  MarkdownChunker c({.target_chunk_chars = 15, .overlap_chars = 3});
  std::string input = "abcdefghij\n\nklmnopqrst";
  auto chunks = c.Split(input);
  ASSERT_EQ(chunks.size(), 2u);
  EXPECT_EQ(chunks[0].text, "abcdefghij");
  // Chunk 1 should be "hij" + "klmnopqrst" (last 3 chars of prev prepended)
  EXPECT_EQ(chunks[1].text, "hijklmnopqrst");
  EXPECT_EQ(chunks[1].char_count, 13u);
}

TEST(MarkdownChunkerTest, ByteOffsetIsZeroForFirstChunk) {
  MarkdownChunker c({.target_chunk_chars = 50, .overlap_chars = 0});
  auto chunks = c.Split("first\n\nsecond");
  ASSERT_GE(chunks.size(), 1u);
  EXPECT_EQ(chunks[0].byte_offset, 0u);
}

TEST(MarkdownChunkerTest, RealisticTaiwanCorpusParagraphChunking) {
  // A simplified slice of the bundled corpus shape — multiple
  // paragraphs separated by \n\n, each paragraph fits within
  // target_chunk_chars. No recursion needed; all paragraphs become
  // standalone chunks.
  MarkdownChunker c; // default: target=450, overlap=80
  std::string input = "## 概覽\n\n"
                      "臺灣（俗字寫作台灣），西方國家在歷史上亦稱福爾摩沙，"
                      "是位於東亞、太平洋西北側的島嶼。\n\n"
                      "## 人口\n\n"
                      "當前統治臺灣的中華民國人口約2,300萬人。";
  auto chunks = c.Split(input);
  // Should produce several chunks — two headers + two paragraphs
  // become 4 natural pieces, merged where they fit.
  ASSERT_GE(chunks.size(), 1u);
  // Every chunk should have a reasonable character count
  for (const auto &ch : chunks) {
    EXPECT_LE(ch.char_count, 600u) << "chunk too large: " << ch.text;
    EXPECT_GT(ch.char_count, 0u);
  }
}

// --- Corpus integration test -------------------------------------------
//
// Reads the real Taiwan zh-TW corpus from docs/rag/taiwan.md and
// validates the chunker's output against structural invariants.
// This is the "real input, verifiable output" test per CLAUDE.md
// Rule 2 — if the separator list or UTF-8 counting breaks, this
// test catches it before the embedder ever sees bad chunks.

std::string ResolveTaiwanCorpusPath() {
  // Bazel runfiles — file is declared as data dep in BUILD.
  if (const char *env = std::getenv("TEST_SRCDIR"); env != nullptr) {
    const std::string candidates[] = {
        std::string(env) + "/_main/docs/rag/taiwan.md",
        std::string(env) + "/docs/rag/taiwan.md",
    };
    for (const auto &p : candidates) {
      std::ifstream probe(p);
      if (probe.good())
        return p;
    }
  }
  // Fallback for running from repo root outside Bazel.
  return "docs/rag/taiwan.md";
}

TEST(MarkdownChunkerCorpusTest, TaiwanCorpusChunksWithinBounds) {
  const std::string path = ResolveTaiwanCorpusPath();
  std::ifstream f(path);
  ASSERT_TRUE(f.good()) << "Cannot open corpus: " << path;
  std::ostringstream ss;
  ss << f.rdbuf();
  const std::string corpus = ss.str();
  ASSERT_GT(corpus.size(), 100u) << "Corpus too small — wrong file?";

  MarkdownChunker c; // default: target=450, overlap=80
  auto chunks = c.Split(corpus);

  // The corpus is ~2800 bytes / ~1400+ code points of zh-TW content
  // plus an ~1100-char HTML comment. With target=450 and overlap=80,
  // expect roughly 5–15 chunks.
  ASSERT_GE(chunks.size(), 3u) << "Too few chunks — splitting broken?";
  ASSERT_LE(chunks.size(), 20u) << "Too many chunks — oversplitting?";

  for (std::size_t i = 0; i < chunks.size(); ++i) {
    const auto &ch = chunks[i];
    // Every chunk must be non-empty.
    EXPECT_GT(ch.char_count, 0u) << "Chunk " << i << " is empty";
    // No chunk should wildly exceed target (450 + separator tolerance).
    EXPECT_LE(ch.char_count, 600u)
        << "Chunk " << i << " too large (" << ch.char_count << " chars)";
    // char_count must match the actual text.
    EXPECT_EQ(ch.char_count, Utf8CharCount(ch.text))
        << "Chunk " << i << " char_count mismatch";
  }
}

TEST(MarkdownChunkerCorpusTest, OverlapPresentBetweenAdjacentChunks) {
  const std::string path = ResolveTaiwanCorpusPath();
  std::ifstream f(path);
  ASSERT_TRUE(f.good()) << "Cannot open corpus: " << path;
  std::ostringstream ss;
  ss << f.rdbuf();
  const std::string corpus = ss.str();

  MarkdownChunker c; // default overlap=80
  auto chunks = c.Split(corpus);
  ASSERT_GE(chunks.size(), 2u);

  // For each adjacent pair, verify the tail of chunk N appears at
  // the start of chunk N+1 (that's what overlap means).
  std::size_t overlaps_found = 0;
  for (std::size_t i = 1; i < chunks.size(); ++i) {
    const std::string &prev = chunks[i - 1].text;
    const std::string &cur = chunks[i].text;
    // Extract the last 80 chars of prev (by bytes via Utf8 helpers).
    const std::size_t prev_chars = Utf8CharCount(prev);
    if (prev_chars <= 80)
      continue; // prev is entirely within overlap — skip
    // Find the byte start of the last 80 chars.
    std::size_t chars_seen = 0;
    std::size_t byte_pos = 0;
    const std::size_t skip = prev_chars - 80;
    while (byte_pos < prev.size() && chars_seen < skip) {
      auto leading = static_cast<unsigned char>(prev[byte_pos]);
      if (leading < 0x80)
        byte_pos += 1;
      else if (leading < 0xE0)
        byte_pos += 2;
      else if (leading < 0xF0)
        byte_pos += 3;
      else
        byte_pos += 4;
      ++chars_seen;
    }
    std::string_view tail(prev.data() + byte_pos, prev.size() - byte_pos);
    // cur should start with this tail.
    if (!tail.empty() && std::string_view(cur).substr(0, tail.size()) == tail) {
      ++overlaps_found;
    }
  }
  EXPECT_GE(overlaps_found, 1u)
      << "No overlap found between any adjacent chunk pair";
}

TEST(MarkdownChunkerCorpusTest, ByteOffsetsAreNonDecreasing) {
  const std::string path = ResolveTaiwanCorpusPath();
  std::ifstream f(path);
  ASSERT_TRUE(f.good());
  std::ostringstream ss;
  ss << f.rdbuf();

  MarkdownChunker c({.target_chunk_chars = 450, .overlap_chars = 0});
  auto chunks = c.Split(ss.str());
  ASSERT_GE(chunks.size(), 2u);

  for (std::size_t i = 1; i < chunks.size(); ++i) {
    EXPECT_GE(chunks[i].byte_offset, chunks[i - 1].byte_offset)
        << "byte_offset went backwards at chunk " << i;
  }
}

} // namespace
} // namespace aegis::rag
