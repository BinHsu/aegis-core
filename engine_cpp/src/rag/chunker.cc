// engine_cpp/src/rag/chunker.cc

#include "engine_cpp/src/rag/chunker.h"

#include <cstddef>
#include <string>
#include <string_view>
#include <utility>
#include <vector>

namespace aegis::rag {

namespace {

// Given a UTF-8 leading byte, return the total byte length of that
// character (1–4). Returns 1 for malformed bytes (continuation bytes
// or invalid leads) to guarantee forward progress.
std::size_t Utf8CharByteLen(unsigned char c) {
  if (c < 0x80)
    return 1; // ASCII
  if (c < 0xC0)
    return 1; // continuation (malformed at a char start — step one)
  if (c < 0xE0)
    return 2; // 2-byte
  if (c < 0xF0)
    return 3; // 3-byte (covers most CJK)
  return 4;   // 4-byte
}

// Return the byte offset at which the first `n` UTF-8 characters end.
// If `s` has fewer than `n` characters, returns s.size().
std::size_t Utf8TakeChars(std::string_view s, std::size_t n) {
  std::size_t i = 0;
  std::size_t chars = 0;
  while (i < s.size() && chars < n) {
    i += Utf8CharByteLen(static_cast<unsigned char>(s[i]));
    ++chars;
  }
  return i > s.size() ? s.size() : i;
}

// Return the byte offset at which we should START to keep the last
// `n` UTF-8 characters of `s`. Iterates backward from the end;
// tolerates malformed UTF-8 by falling back to the beginning of s.
std::size_t Utf8DropAllButLastChars(std::string_view s, std::size_t n) {
  if (Utf8CharCount(s) <= n)
    return 0;
  // Walk forward, counting characters, stop at (total - n).
  const std::size_t total = Utf8CharCount(s);
  return Utf8TakeChars(s, total - n);
}

// Split `text` by `sep` (a non-empty delimiter). Empty `sep` is
// undefined; use the atomic character-level split below for that.
// Each result is a view into `text` — the caller must keep `text`
// alive for the lifetime of the returned views.
std::vector<std::string_view> SplitBy(std::string_view text,
                                      std::string_view sep) {
  std::vector<std::string_view> out;
  if (sep.empty()) {
    out.push_back(text);
    return out;
  }
  std::size_t pos = 0;
  while (pos <= text.size()) {
    const std::size_t found = text.find(sep, pos);
    if (found == std::string_view::npos) {
      out.push_back(text.substr(pos));
      break;
    }
    out.push_back(text.substr(pos, found - pos));
    pos = found + sep.size();
  }
  return out;
}

// Split `text` into character-sized pieces of at most `max_chars`
// UTF-8 code points. This is the "empty separator" fallback: used
// only when NO separator in the priority list appears in the text.
std::vector<std::string_view> CharSplit(std::string_view text,
                                        std::size_t max_chars) {
  std::vector<std::string_view> out;
  std::size_t pos = 0;
  while (pos < text.size()) {
    const std::size_t take =
        Utf8TakeChars(std::string_view(text).substr(pos), max_chars);
    out.push_back(text.substr(pos, take));
    pos += take;
  }
  return out;
}

// Core recursive splitter. `text` is a substring of the original
// corpus; `base_byte_offset` is where `text` begins in the original
// corpus (used to compute each chunk's byte_offset). `seps` is the
// separator sub-list to try for this level of recursion.
//
// Output: a flat list of chunks with their original-corpus byte
// offsets attached. Overlap is NOT applied here (the caller does
// a second pass to splice overlap at chunk boundaries).
std::vector<Chunk> SplitRecursive(std::string_view text,
                                  std::size_t base_byte_offset,
                                  std::size_t target_chars,
                                  const std::vector<std::string> &seps) {
  std::vector<Chunk> out;
  if (text.empty())
    return out;

  // Base case — text already fits.
  const std::size_t text_chars = Utf8CharCount(text);
  if (text_chars <= target_chars) {
    out.push_back(Chunk{std::string(text), base_byte_offset, text_chars});
    return out;
  }

  // Find the first separator that appears in text.
  std::size_t sep_index = seps.size();
  for (std::size_t i = 0; i < seps.size(); ++i) {
    if (text.find(seps[i]) != std::string_view::npos) {
      sep_index = i;
      break;
    }
  }

  std::vector<std::string_view> pieces;
  std::string chosen_sep;
  if (sep_index < seps.size()) {
    chosen_sep = seps[sep_index];
    pieces = SplitBy(text, chosen_sep);
  } else {
    // Last resort — character-level splitting. No separator string
    // to re-join with.
    pieces = CharSplit(text, target_chars);
  }

  // Merge adjacent pieces into target-sized chunks, recursing on
  // pieces that individually exceed target.
  std::string buf;
  std::size_t buf_byte_offset = base_byte_offset;
  std::size_t buf_chars = 0;
  std::size_t running_byte_offset = base_byte_offset;

  auto flush = [&]() {
    if (!buf.empty()) {
      out.push_back(Chunk{std::move(buf), buf_byte_offset, buf_chars});
      buf.clear();
      buf_chars = 0;
    }
  };

  for (std::size_t i = 0; i < pieces.size(); ++i) {
    const std::string_view p = pieces[i];
    const std::size_t p_chars = Utf8CharCount(p);

    if (p_chars > target_chars && sep_index + 1 < seps.size()) {
      // This piece alone is oversized — flush what we have, then
      // recurse with the remaining separators.
      flush();
      buf_byte_offset = running_byte_offset + chosen_sep.size() * (i > 0);
      std::vector<std::string> remaining(seps.begin() + sep_index + 1,
                                         seps.end());
      const std::size_t p_byte_offset =
          static_cast<std::size_t>(p.data() - text.data()) + base_byte_offset;
      auto sub = SplitRecursive(p, p_byte_offset, target_chars, remaining);
      for (auto &c : sub) {
        out.push_back(std::move(c));
      }
      running_byte_offset = static_cast<std::size_t>(p.data() - text.data()) +
                            p.size() + base_byte_offset;
      buf_byte_offset = running_byte_offset + chosen_sep.size();
      continue;
    }

    // Would adding `p` to the current buffer exceed target?
    const std::size_t sep_chars =
        (!buf.empty() && !chosen_sep.empty()) ? Utf8CharCount(chosen_sep) : 0;
    if (buf_chars + sep_chars + p_chars > target_chars) {
      flush();
      buf_byte_offset =
          static_cast<std::size_t>(p.data() - text.data()) + base_byte_offset;
    }

    if (buf.empty()) {
      buf_byte_offset =
          static_cast<std::size_t>(p.data() - text.data()) + base_byte_offset;
      buf.append(p);
      buf_chars = p_chars;
    } else {
      buf.append(chosen_sep);
      buf.append(p);
      buf_chars += sep_chars + p_chars;
    }
    running_byte_offset = static_cast<std::size_t>(p.data() - text.data()) +
                          p.size() + base_byte_offset;
  }
  flush();
  return out;
}

// Second pass: apply overlap by prepending the last `overlap_chars`
// UTF-8 code points of chunk N to the front of chunk N+1.
// Byte offset for chunk N+1 moves back by the overlap length.
void ApplyOverlap(std::vector<Chunk> &chunks, std::size_t overlap_chars) {
  if (overlap_chars == 0 || chunks.size() < 2)
    return;
  for (std::size_t i = 1; i < chunks.size(); ++i) {
    const Chunk &prev = chunks[i - 1];
    const std::size_t overlap_start_byte_in_prev =
        Utf8DropAllButLastChars(prev.text, overlap_chars);
    const std::string_view tail =
        std::string_view(prev.text).substr(overlap_start_byte_in_prev);
    const std::size_t tail_chars = Utf8CharCount(tail);

    Chunk &cur = chunks[i];
    std::string stitched;
    stitched.reserve(tail.size() + cur.text.size());
    stitched.append(tail);
    stitched.append(cur.text);
    cur.text = std::move(stitched);
    // byte_offset shifts earlier by the tail's byte length
    cur.byte_offset =
        cur.byte_offset >= tail.size() ? cur.byte_offset - tail.size() : 0;
    cur.char_count += tail_chars;
  }
}

} // namespace

std::size_t Utf8CharCount(std::string_view s) {
  std::size_t n = 0;
  std::size_t i = 0;
  while (i < s.size()) {
    i += Utf8CharByteLen(static_cast<unsigned char>(s[i]));
    ++n;
  }
  return n;
}

MarkdownChunker::MarkdownChunker() : config_({}) {}

MarkdownChunker::MarkdownChunker(Config config) : config_(std::move(config)) {}

std::vector<Chunk> MarkdownChunker::Split(std::string_view markdown) const {
  if (markdown.empty())
    return {};
  auto chunks = SplitRecursive(markdown, /*base_byte_offset=*/0,
                               config_.target_chunk_chars, config_.separators);
  ApplyOverlap(chunks, config_.overlap_chars);
  return chunks;
}

} // namespace aegis::rag
