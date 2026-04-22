// engine_cpp/tests/unit/engine_seed_util_test.cc
//
// Unit coverage for the pure helpers exposed by
// engine_cpp/cmd/engine/seed.h — ContentHashUuid + DeriveCollectionName.
// These are the pieces that can be exercised without a running Qdrant
// instance or an embedder model; end-to-end seed behavior is covered
// in the integration test gated on QDRANT_URL + AEGIS_MODEL_DIR.

#include "engine_cpp/cmd/engine/seed.h"

#include <string>

#include "gtest/gtest.h"

namespace aegis::engine_cmd {
namespace {

// -----------------------------------------------------------------------------
// ContentHashUuid — deterministic UUID from SHA-256 of the text.
// -----------------------------------------------------------------------------

TEST(ContentHashUuidTest, IsDeterministicOnSameText) {
  const std::string text = "The quick brown fox jumps over the lazy dog.";
  const auto a = ContentHashUuid(text);
  const auto b = ContentHashUuid(text);
  EXPECT_EQ(a, b);
}

TEST(ContentHashUuidTest, DiffersForDifferentText) {
  const auto a = ContentHashUuid("Hello, world.");
  const auto b = ContentHashUuid("Hello, world!"); // one byte different
  EXPECT_NE(a, b);
}

TEST(ContentHashUuidTest, OutputIsCanonicalUuidFormat) {
  // Shape: xxxxxxxx-xxxx-Vxxx-Nxxx-xxxxxxxxxxxx where V=5 (version
  // nibble) and N is one of 8/9/a/b (RFC 4122 variant high bits 10xx).
  const auto uuid = ContentHashUuid("payload");
  ASSERT_EQ(uuid.size(), 36u);
  EXPECT_EQ(uuid[8], '-');
  EXPECT_EQ(uuid[13], '-');
  EXPECT_EQ(uuid[18], '-');
  EXPECT_EQ(uuid[23], '-');
  EXPECT_EQ(uuid[14], '5'); // version-5 nibble
  const char variant = uuid[19];
  EXPECT_TRUE(variant == '8' || variant == '9' || variant == 'a' ||
              variant == 'b')
      << "RFC 4122 variant high nibble must be 8/9/a/b, got " << variant;
  // Every other character must be lowercase hex.
  for (size_t i = 0; i < uuid.size(); ++i) {
    if (i == 8 || i == 13 || i == 18 || i == 23)
      continue;
    const char c = uuid[i];
    EXPECT_TRUE((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f'))
        << "non-hex char at index " << i << ": " << c;
  }
}

TEST(ContentHashUuidTest, EmptyStringHasDefinedOutput) {
  // SHA-256("") is a well-known constant — the output is stable, not
  // a random exception. The seed pipeline rejects empty chunk text
  // upstream of this function, but the UUID derivation itself should
  // not throw or UB on empty input.
  const auto uuid = ContentHashUuid("");
  EXPECT_EQ(uuid.size(), 36u);
  EXPECT_EQ(uuid[14], '5');
}

// -----------------------------------------------------------------------------
// DeriveCollectionName — basename + sanitize + `aegis_<tenant>_` prefix.
// -----------------------------------------------------------------------------

TEST(DeriveCollectionNameTest, StripsDirectoryAndExtension) {
  EXPECT_EQ(DeriveCollectionName("docs/rag/taiwan.md", "demo"),
            "aegis_demo_taiwan");
}

TEST(DeriveCollectionNameTest, NoDirectoryStillWorks) {
  EXPECT_EQ(DeriveCollectionName("corpus.md", "demo"), "aegis_demo_corpus");
}

TEST(DeriveCollectionNameTest, NoExtensionStillWorks) {
  EXPECT_EQ(DeriveCollectionName("corpus", "demo"), "aegis_demo_corpus");
}

TEST(DeriveCollectionNameTest, LowercasesCapitals) {
  EXPECT_EQ(DeriveCollectionName("MixedCaseName.md", "demo"),
            "aegis_demo_mixedcasename");
}

TEST(DeriveCollectionNameTest, MapsUnsafeCharactersToUnderscore) {
  // Hyphens, dots (in stem), and spaces all collapse to '_'.
  EXPECT_EQ(DeriveCollectionName("foo-bar.v2.md", "demo"),
            "aegis_demo_foo_bar_v2");
  EXPECT_EQ(DeriveCollectionName("a b c.md", "demo"), "aegis_demo_a_b_c");
}

TEST(DeriveCollectionNameTest, EmptyStemFallsBack) {
  // `.md` has no stem after stripping the extension.
  EXPECT_EQ(DeriveCollectionName(".md", "demo"), "aegis_demo_unnamed");
}

TEST(DeriveCollectionNameTest, AbsolutePathWorks) {
  EXPECT_EQ(DeriveCollectionName("/tmp/Corpus-Name.V1.md", "demo"),
            "aegis_demo_corpus_name_v1");
}

TEST(DeriveCollectionNameTest, TenantSegmentIsSanitized) {
  // Tenant with unsafe chars: uppercase + hyphen + slash.
  EXPECT_EQ(DeriveCollectionName("taiwan.md", "Acme-Corp/team"),
            "aegis_acme_corp_team_taiwan");
}

TEST(DeriveCollectionNameTest, EmptyTenantFallsBackToDemo) {
  EXPECT_EQ(DeriveCollectionName("taiwan.md", ""), "aegis_demo_taiwan");
}

} // namespace
} // namespace aegis::engine_cmd
