// engine_cpp/src/rag/chunker.h
//
// Chinese-aware recursive character text splitter for RAG corpora.
// Implements the chunking rules ADR-0019 §Decision.2 defined and
// ADR-0020 preserved: target ~450 characters per chunk, ~80 character
// overlap, separator priority list ["\n\n", "\n", "。", "！", "？",
// "，", " ", ""] — trying each in turn and falling through to
// character-level splitting only as a last resort.
//
// Character counting is UTF-8 code points, NOT bytes. One zh-TW
// character is typically 3 bytes in UTF-8, so a naive byte-based
// splitter would produce chunks a third of the intended size — bad
// for embedder context utilization and demonstrably worse for
// retrieval quality on zh-TW corpora (which is exactly the Phase 3
// demo target). The Utf8CharCount helper and the byte-offset
// bookkeeping in `Chunk` ensure chunks are sized in characters while
// source positions are reportable in bytes.
//
// Thread safety: MarkdownChunker is const-safe — Split() holds no
// instance state beyond the Config passed to the constructor.
// Callers can share one instance across threads.

#ifndef AEGIS_ENGINE_CPP_SRC_RAG_CHUNKER_H_
#define AEGIS_ENGINE_CPP_SRC_RAG_CHUNKER_H_

#include <cstddef>
#include <string>
#include <string_view>
#include <vector>

namespace aegis::rag {

// A single chunk emitted by MarkdownChunker.
struct Chunk {
  // Chunk text content (owned). UTF-8 encoded.
  std::string text;

  // Byte offset of this chunk's first byte in the source corpus.
  // When overlap is in play, this is the offset of the overlap
  // region's start (i.e. earlier than the chunk's "new content"
  // begins) — treat it as a traceability hint, not a strict
  // partition boundary.
  std::size_t byte_offset = 0;

  // Character count (UTF-8 code points), for observability / sanity
  // checks. Should be <= Config::target_chunk_chars in the common
  // case; may exceed it when a single atomic piece (e.g. a paragraph
  // with no internal separators) doesn't fit.
  std::size_t char_count = 0;
};

class MarkdownChunker {
public:
  struct Config {
    // Target chunk size in UTF-8 code points. 450 is the ADR-0019
    // default — comfortably below bge-m3's 512-token effective
    // context window (zh characters are typically 1–2 BPE tokens).
    std::size_t target_chunk_chars = 450;

    // Overlap in UTF-8 code points. The last `overlap_chars` of
    // chunk N become the first characters of chunk N+1, to avoid
    // cross-sentence boundaries cutting semantic unity. 80 is the
    // ADR-0019 default. Set to 0 to disable overlap.
    std::size_t overlap_chars = 80;

    // Separator priority list. The splitter tries each separator
    // in order; the first one present in the input is used for
    // that level. Empty string "" is the implicit last resort
    // (character-level splitting) and does not need to appear
    // here — Split() handles it automatically.
    std::vector<std::string> separators = {"\n\n", "\n", "。", "！",
                                           "？",   "，", " "};
  };

  MarkdownChunker();
  explicit MarkdownChunker(Config config);

  // Split the input markdown into chunks per the Config. Never
  // throws; pathological input (all-whitespace, single huge
  // paragraph with no separators) produces a best-effort result.
  // The output preserves the source order — chunk[i]'s content
  // appears before chunk[i+1]'s in the source (modulo overlap).
  std::vector<Chunk> Split(std::string_view markdown) const;

private:
  Config config_;
};

// Public for testability — UTF-8 code point counter.
std::size_t Utf8CharCount(std::string_view s);

} // namespace aegis::rag

#endif // AEGIS_ENGINE_CPP_SRC_RAG_CHUNKER_H_
