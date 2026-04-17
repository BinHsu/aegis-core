// engine_cpp/cmd/engine/seed.h
//
// `engine seed --corpus PATH --target={local|cloud} [--verbose]`
// subcommand implementation (Phase 3b Slice 6).
//
// End-to-end pipeline: read a markdown corpus → MarkdownChunker →
// GGMLEmbedder (bge-m3 Q4_K_M) → QdrantClient CreateCollection +
// UpsertPoints. Returns an exit code suitable for `int main()`.
//
// Design decisions (2026-04-17):
//   - Subcommand dispatch (`engine seed` vs default `engine` server
//     mode) instead of a top-level `--seed` flag, so later subcommands
//     (migrate, repair-index, validate) slot in cleanly.
//   - Point IDs are content-hashed: SHA-256 of the chunk text, first
//     16 bytes formatted as a UUID5-style string. Deterministic across
//     re-seeding runs → Qdrant's idempotent upsert semantics dedupe
//     duplicate text across corpora for free.
//   - Collection name is `aegis_<stem>` derived from the corpus
//     basename. One collection per corpus file.
//   - Payload is the minimal three fields the search path needs:
//     `text`, `source_path`, `chunk_index`. Richer metadata on demand.
//   - `--target=local` uses localhost:6334 plaintext by default;
//     `--target=cloud` reads QDRANT_URL + QDRANT_API_KEY via
//     QdrantClient::ConfigFromEnv().

#ifndef AEGIS_ENGINE_CPP_CMD_ENGINE_SEED_H_
#define AEGIS_ENGINE_CPP_CMD_ENGINE_SEED_H_

#include <string>
#include <string_view>

namespace aegis::engine_cmd {

// Entry point for the `seed` subcommand. `argc`/`argv` should have the
// subcommand argv[0] ("seed") already stripped by main.cc — absl::flags
// parses the remaining tokens. Returns 0 on success, non-zero on
// failure; suitable to return from `int main()`.
int RunSeed(int argc, char **argv);

// Derive a deterministic UUID-formatted point ID from the chunk text.
// SHA-256(text) → first 16 bytes → set RFC 4122 version-5 + variant
// bits → format as `xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx`. Identical
// text always produces the same UUID, so re-seeding is idempotent at
// the Qdrant upsert layer. Exposed for unit testing.
std::string ContentHashUuid(std::string_view text);

// Derive a Qdrant collection name from a corpus file path:
//   docs/rag/taiwan.md  → aegis_taiwan
//   /tmp/foo-bar.v2.md  → aegis_foo_bar_v2
// Takes the basename, strips the extension, lowercases A–Z, and maps
// any character outside `[a-z0-9_]` to `_`. Empty stems fall back to
// `aegis_unnamed`. Exposed for unit testing.
std::string DeriveCollectionName(std::string_view corpus_path);

} // namespace aegis::engine_cmd

#endif // AEGIS_ENGINE_CPP_CMD_ENGINE_SEED_H_
