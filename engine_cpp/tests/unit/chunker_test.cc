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

  // The corpus is zh-TW content plus a ~1400-char HTML comment, with
  // roughly a dozen `##` sub-section headers each scoped to one
  // sub-topic. With target=450 + overlap=80 + markdown-aware
  // segmentation, each section becomes its own chunk. Expected range
  // covers the header count plus comment-split chunks with headroom
  // for future corpus evolution.
  ASSERT_GE(chunks.size(), 3u) << "Too few chunks — splitting broken?";
  ASSERT_LE(chunks.size(), 30u) << "Too many chunks — oversplitting?";

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

  MarkdownChunker c; // default overlap=80, boundary_search=40
  auto chunks = c.Split(corpus);
  ASSERT_GE(chunks.size(), 2u);

  // Verify the minimum contract: SOME non-empty suffix of chunk N-1
  // appears as a prefix of chunk N. (Exact tail length varies once
  // the sentence-aware overlap walk is in play — the walk may shorten
  // the tail to end cleanly at `。` / `\n`. "At least one adjacent
  // pair overlaps by at least one byte" is the stable invariant; the
  // exact size isn't.) Byte-level comparison — overlap is always
  // copied byte-exact, so any suffix/prefix byte match is genuine.
  std::size_t overlaps_found = 0;
  for (std::size_t i = 1; i < chunks.size(); ++i) {
    const std::string_view prev = chunks[i - 1].text;
    const std::string_view cur = chunks[i].text;
    const std::size_t max_len = std::min(prev.size(), cur.size());
    for (std::size_t len = 1; len <= max_len; ++len) {
      if (prev.substr(prev.size() - len) == cur.substr(0, len)) {
        ++overlaps_found;
        break;
      }
    }
  }
  EXPECT_GE(overlaps_found, 1u)
      << "No overlap found between any adjacent chunk pair";
}

// --- Rework tests (2026-04-22): markdown-aware + sentence-aware overlap ---

TEST(MarkdownChunkerTest, HeaderIsHardSegmentBoundary) {
  // Section A has enough content that, with the v1 separator-only
  // cascade, overlap would pull its tail across `## B` into the
  // second chunk's prefix (producing "...A tail...## B B body").
  // With respect_markdown_headers=true (default), the second chunk
  // must NOT contain any text from section A.
  MarkdownChunker c; // default: respect_markdown_headers = true
  std::string input =
      "## A\n\n"
      "A body sentence one. A body sentence two. A body sentence three.\n\n"
      "## B\n\n"
      "B body sentence one. B body sentence two.";
  auto chunks = c.Split(input);
  ASSERT_GE(chunks.size(), 2u);

  // The chunk containing "## B" must NOT contain any of "A body".
  bool found_b_chunk = false;
  for (const auto &ch : chunks) {
    if (ch.text.find("## B") != std::string::npos) {
      found_b_chunk = true;
      EXPECT_EQ(ch.text.find("A body"), std::string::npos)
          << "Header hard-boundary violated — chunk crossed `## B`:\n"
          << ch.text;
    }
  }
  EXPECT_TRUE(found_b_chunk) << "No chunk contained `## B`";
}

TEST(MarkdownChunkerTest, OverlapWalksForwardToSentenceStart) {
  // Construct zh-TW input where chunk 0 contains a full sentence
  // mid-body (with `。` in its interior, not at its end) — so the
  // raw N-char tail lands mid-sentence, and walking forward should
  // advance to the next `。`.
  //
  // Chunker trace at target=20, sep `\n\n` splits:
  //   Piece 0: "超長第一句內容。超長第二句內容。" (16 chars)
  //   Piece 1: "第三段。" (4 chars)
  //   Merge: buf="超長第一句內容。超長第二句內容。" (16) — adding
  //   Piece 1 with sep "\n\n" gives 16+2+4=22 > 20 → flush. Chunk
  //   0 = "超長第一句內容。超長第二句內容。". Chunk 1 = "第三段。".
  //
  // Overlap pass (overlap=10, search=20):
  //   Chunk 0 has 16 chars. Raw last-10-char tail starts at char 6
  //   = byte 18 → tail begins mid-sentence at "容。超長第二句內容。".
  //   Walk forward: first `。` at byte 21. boundary_start = 24.
  //   Walked tail = "超長第二句內容。" (8 chars) — clean sentence
  //   start.
  //
  // Assertion: chunk[1] must NOT contain "容。超" (raw-tail marker)
  // — it must begin with the post-`。` "超長第二".
  MarkdownChunker c({.target_chunk_chars = 20,
                     .overlap_chars = 10,
                     .respect_markdown_headers = false,
                     .overlap_boundary_search_chars = 20});
  std::string input = "超長第一句內容。超長第二句內容。\n\n第三段。";
  auto chunks = c.Split(input);
  ASSERT_GE(chunks.size(), 2u);

  const std::string &second = chunks[1].text;
  EXPECT_EQ(second.find("容。超"), std::string::npos)
      << "Second chunk starts mid-sentence — walk did not advance past `。`:\n"
      << second;
  EXPECT_EQ(second.find("超長第二"), 0u)
      << "Second chunk does not start at the post-`。` clean sentence:\n"
      << second;
}

TEST(MarkdownChunkerTest, OverlapDoesNotCrossHeader) {
  // Two sections. Section A's trailing sentences are exactly the
  // text we'd expect overlap to grab. With header respect on, no
  // chunk in section B may contain any of section A's content —
  // not even in its overlap prefix.
  MarkdownChunker c({.target_chunk_chars = 30,
                     .overlap_chars = 20,
                     .respect_markdown_headers = true,
                     .overlap_boundary_search_chars = 0});
  std::string input = "## A\n\nsection A unique marker AAAA.\n\n"
                      "## B\n\nsection B unique marker BBBB.";
  auto chunks = c.Split(input);
  ASSERT_GE(chunks.size(), 2u);

  for (const auto &ch : chunks) {
    if (ch.text.find("BBBB") != std::string::npos) {
      EXPECT_EQ(ch.text.find("AAAA"), std::string::npos)
          << "Overlap crossed `## B` header:\n"
          << ch.text;
    }
  }
}

TEST(MarkdownChunkerTest, RespectMarkdownHeadersFalseRestoresV1) {
  // When the flag is off, headers are just text. Confirm the
  // splitter merges across headers like before — this is the
  // explicit escape hatch for plain-text corpora where `#` has no
  // semantic meaning.
  MarkdownChunker c({.target_chunk_chars = 200,
                     .overlap_chars = 0,
                     .respect_markdown_headers = false,
                     .overlap_boundary_search_chars = 0});
  std::string input = "## A\n\nshort body.\n\n## B\n\nanother short body.";
  auto chunks = c.Split(input);
  // Without segmentation, this whole input fits in one chunk
  // (target=200, total < 60 chars) — so we get exactly one chunk.
  // With segmentation, we'd get at least 3.
  ASSERT_EQ(chunks.size(), 1u);
  EXPECT_NE(chunks[0].text.find("## A"), std::string::npos);
  EXPECT_NE(chunks[0].text.find("## B"), std::string::npos);
}

TEST(MarkdownChunkerCorpusTest, TaiwanCorpusNoChunkSpansTwoHeaders) {
  // Regression for the 2026-04-21 LAN smoke finding: a chunk's text
  // included "130公里...## 地理與氣候臺灣島的總..." — overlap from
  // the "概覽" section bleeding across `## 地理與氣候` into the next
  // section's content. With respect_markdown_headers=true, no chunk
  // may contain more than one `##` header line.
  const std::string path = ResolveTaiwanCorpusPath();
  std::ifstream f(path);
  ASSERT_TRUE(f.good()) << "Cannot open corpus: " << path;
  std::ostringstream ss;
  ss << f.rdbuf();
  const std::string corpus = ss.str();

  MarkdownChunker c; // default
  auto chunks = c.Split(corpus);
  ASSERT_GE(chunks.size(), 2u);

  for (std::size_t i = 0; i < chunks.size(); ++i) {
    const std::string &t = chunks[i].text;
    std::size_t header_count = 0;
    // Count occurrences of `\n## ` plus a starting `## ` if the
    // chunk begins with a header line.
    if (t.size() >= 3 && t.substr(0, 3) == "## ")
      ++header_count;
    std::size_t pos = 0;
    while ((pos = t.find("\n## ", pos)) != std::string::npos) {
      ++header_count;
      ++pos;
    }
    EXPECT_LE(header_count, 1u)
        << "Chunk " << i << " spans " << header_count
        << " `## ` headers — overlap crossed a section boundary:\n"
        << t;
  }
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
