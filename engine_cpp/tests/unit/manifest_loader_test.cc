// engine_cpp/tests/unit/manifest_loader_test.cc
//
// Unit tests for the CAS preflight walker (ADR-0026 §Engine responsibilities).
// Each test writes a tiny manifest + test files under $TEST_TMPDIR and
// exercises a specific layer of the walker (stat / size / SHA).

#include "engine_cpp/src/models/manifest_loader.h"

#include <cstdlib>
#include <filesystem>
#include <fstream>
#include <string>

#include "absl/status/status.h"
#include "gtest/gtest.h"

namespace aegis::models {
namespace {

namespace fs = std::filesystem;

// Resolve TEST_TMPDIR (set by Bazel for cc_test) or fall back to /tmp.
fs::path TestTmpRoot() {
  if (const char *env = std::getenv("TEST_TMPDIR"); env != nullptr) {
    return fs::path(env);
  }
  return fs::temp_directory_path();
}

// Write `content` to `path`, creating parent dirs as needed.
void WriteFile(const fs::path &path, const std::string &content) {
  fs::create_directories(path.parent_path());
  std::ofstream f(path, std::ios::binary);
  f << content;
}

// Pre-computed SHA-256 of the literal string "hello\n" (with trailing newline).
// Computed via: printf 'hello\n' | shasum -a 256
// = 5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03
constexpr char kHelloContent[] = "hello\n";
constexpr char kHelloSha[] =
    "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03";
constexpr std::int64_t kHelloSize = 6; // "hello\n"

// Build a minimal manifest JSON string with one entry.
std::string MakeManifestJson(const std::string &id, const std::string &filename,
                             const std::string &sha256, std::int64_t size_bytes,
                             bool required) {
  return R"({
    "schema_version": 1,
    "models": [
      {
        "id": ")" +
         id + R"(",
        "filename": ")" +
         filename + R"(",
        "type": "transcription",
        "sha256": ")" +
         sha256 + R"(",
        "size_bytes": )" +
         std::to_string(size_bytes) + R"(,
        "required": )" +
         (required ? "true" : "false") + R"(
      }
    ]
  })";
}

// ----- ResolveCasPath -----

TEST(ResolveCasPath, ComposesRootIdShaExt) {
  ModelEntry e;
  e.id = "whisper-tiny-en";
  e.filename = "ggml-tiny.en.bin";
  e.sha256 = "921e4cf8686fdd993dcd081a5da5b6c365bfde1162e72b08d75ac75289920b1f";

  EXPECT_EQ(
      ResolveCasPath("/models", e),
      "/models/whisper-tiny-en/"
      "921e4cf8686fdd993dcd081a5da5b6c365bfde1162e72b08d75ac75289920b1f.bin");
}

TEST(ResolveCasPath, UsesLastDotAsExtension) {
  ModelEntry e;
  e.id = "foo";
  e.filename = "some.model.with.dots.gguf";
  e.sha256 = "deadbeef";

  // Last '.' determines extension → ".gguf".
  EXPECT_EQ(ResolveCasPath("/root", e), "/root/foo/deadbeef.gguf");
}

TEST(ResolveCasPath, HandlesFilenameWithoutExtension) {
  ModelEntry e;
  e.id = "foo";
  e.filename = "no_extension_at_all";
  e.sha256 = "abc";

  EXPECT_EQ(ResolveCasPath("/root", e), "/root/foo/abc");
}

// ----- ComputeFileSha256 -----

TEST(ComputeFileSha256, MatchesKnownHash) {
  const fs::path f = TestTmpRoot() / "sha_test" / "hello.txt";
  WriteFile(f, kHelloContent);

  EXPECT_EQ(ComputeFileSha256(f.string()), kHelloSha);
}

TEST(ComputeFileSha256, ReturnsEmptyOnMissingFile) {
  EXPECT_EQ(ComputeFileSha256("/nonexistent/path/to/nothing"), "");
}

// ----- LoadManifest -----

TEST(LoadManifest, ParsesValidSingleEntry) {
  const fs::path m = TestTmpRoot() / "manifest_valid" / "manifest.json";
  WriteFile(m, MakeManifestJson("whisper-tiny-en", "ggml-tiny.en.bin",
                                "921e4cf8", kHelloSize, /*required=*/true));

  auto result = LoadManifest(m.string());
  ASSERT_TRUE(result.ok()) << result.status();
  EXPECT_EQ(result->schema_version, 1);
  ASSERT_EQ(result->models.size(), 1u);
  EXPECT_EQ(result->models[0].id, "whisper-tiny-en");
  EXPECT_EQ(result->models[0].filename, "ggml-tiny.en.bin");
  EXPECT_EQ(result->models[0].sha256, "921e4cf8");
  EXPECT_EQ(result->models[0].size_bytes, kHelloSize);
  EXPECT_TRUE(result->models[0].required);
}

TEST(LoadManifest, FailsOnMissingFile) {
  auto result = LoadManifest("/nonexistent/manifest.json");
  ASSERT_FALSE(result.ok());
  EXPECT_EQ(result.status().code(), absl::StatusCode::kNotFound);
}

TEST(LoadManifest, FailsOnInvalidJson) {
  const fs::path m = TestTmpRoot() / "manifest_bad_json" / "manifest.json";
  WriteFile(m, "{ this is not valid json }");

  auto result = LoadManifest(m.string());
  ASSERT_FALSE(result.ok());
  EXPECT_EQ(result.status().code(), absl::StatusCode::kInvalidArgument);
}

TEST(LoadManifest, FailsOnMissingRequiredField) {
  const fs::path m = TestTmpRoot() / "manifest_missing_field" / "manifest.json";
  // Manifest with a model entry that lacks the `sha256` field.
  WriteFile(m, R"({
    "schema_version": 1,
    "models": [
      {
        "id": "test",
        "filename": "test.bin",
        "size_bytes": 0,
        "required": false
      }
    ]
  })");

  auto result = LoadManifest(m.string());
  ASSERT_FALSE(result.ok());
  EXPECT_EQ(result.status().code(), absl::StatusCode::kInvalidArgument);
}

// ----- VerifyEntry — the three failure layers + success path -----

TEST(VerifyEntry, SuccessOnMatchingStatSizeSha) {
  const fs::path root = TestTmpRoot() / "verify_success";
  fs::create_directories(root);

  ModelEntry e;
  e.id = "hello-model";
  e.filename = "hello.txt";
  e.sha256 = kHelloSha;
  e.size_bytes = kHelloSize;
  e.required = true;

  WriteFile(root / "hello-model" / (std::string(kHelloSha) + ".txt"),
            kHelloContent);

  VerifyResult r = VerifyEntry(root.string(), e);
  EXPECT_TRUE(r.ok) << r.diagnostic;
  EXPECT_EQ(r.id, "hello-model");
}

TEST(VerifyEntry, FailsOnMissingFile) {
  const fs::path root = TestTmpRoot() / "verify_missing";
  fs::create_directories(root);

  ModelEntry e;
  e.id = "nope";
  e.filename = "nope.bin";
  e.sha256 = kHelloSha;
  e.size_bytes = kHelloSize;
  e.required = true;

  VerifyResult r = VerifyEntry(root.string(), e);
  EXPECT_FALSE(r.ok);
  EXPECT_NE(r.diagnostic.find("MISSING"), std::string::npos)
      << "diagnostic: " << r.diagnostic;
}

TEST(VerifyEntry, FailsOnSizeMismatch) {
  const fs::path root = TestTmpRoot() / "verify_size_mismatch";
  fs::create_directories(root);

  ModelEntry e;
  e.id = "short-one";
  e.filename = "short.bin";
  e.sha256 = kHelloSha;  // would match hello content
  e.size_bytes = 999999; // but expect 999999 bytes, not 6
  e.required = true;

  WriteFile(root / "short-one" / (std::string(kHelloSha) + ".bin"),
            kHelloContent);

  VerifyResult r = VerifyEntry(root.string(), e);
  EXPECT_FALSE(r.ok);
  EXPECT_NE(r.diagnostic.find("WRONG SIZE"), std::string::npos)
      << "diagnostic: " << r.diagnostic;
}

TEST(VerifyEntry, FailsOnShaMismatch) {
  const fs::path root = TestTmpRoot() / "verify_sha_mismatch";
  fs::create_directories(root);

  // File content is "hello\n" (sha kHelloSha) but manifest claims a
  // different sha. Same size, so we pass layer 2 and fail at layer 3.
  constexpr char kFakeSha[] =
      "0000000000000000000000000000000000000000000000000000000000000000";
  ModelEntry e;
  e.id = "wrong-sha";
  e.filename = "wrong.bin";
  e.sha256 = kFakeSha;
  e.size_bytes = kHelloSize;
  e.required = true;

  WriteFile(root / "wrong-sha" / (std::string(kFakeSha) + ".bin"),
            kHelloContent);

  VerifyResult r = VerifyEntry(root.string(), e);
  EXPECT_FALSE(r.ok);
  EXPECT_NE(r.diagnostic.find("SHA-256 MISMATCH"), std::string::npos)
      << "diagnostic: " << r.diagnostic;
}

// ----- VerifyAllRequired — aggregate behavior -----

TEST(VerifyAllRequired, SkipsNonRequiredEntries) {
  // Non-required entries must NOT be touched even if they would fail.
  const fs::path root = TestTmpRoot() / "verify_all_skip_optional";
  fs::create_directories(root);

  Manifest m;
  m.schema_version = 1;
  ModelEntry opt;
  opt.id = "would-fail";
  opt.filename = "missing.bin";
  opt.sha256 = kHelloSha;
  opt.size_bytes = kHelloSize;
  opt.required = false; // optional — walker must skip
  m.models.push_back(opt);

  EXPECT_TRUE(VerifyAllRequired(root.string(), m).ok());
}

TEST(VerifyAllRequired, ToleratesExtraFilesOnDisk) {
  // ADR-0026 line 89: walker must tolerate extra objects from other
  // concurrently-deployed engine versions.
  const fs::path root = TestTmpRoot() / "verify_all_extras";
  fs::create_directories(root);

  // Stray file not mentioned in manifest — should not cause failure.
  WriteFile(root / "stranger-model" / "deadbeef.bin", "stuff");

  Manifest m;
  m.schema_version = 1;
  // Only one required entry, and it's present:
  ModelEntry e;
  e.id = "hello-model";
  e.filename = "hello.txt";
  e.sha256 = kHelloSha;
  e.size_bytes = kHelloSize;
  e.required = true;
  m.models.push_back(e);

  WriteFile(root / "hello-model" / (std::string(kHelloSha) + ".txt"),
            kHelloContent);

  EXPECT_TRUE(VerifyAllRequired(root.string(), m).ok());
}

TEST(VerifyAllRequired, ReturnsNotFoundWhenMissing) {
  const fs::path root = TestTmpRoot() / "verify_all_notfound";
  fs::create_directories(root);

  Manifest m;
  m.schema_version = 1;
  ModelEntry e;
  e.id = "missing";
  e.filename = "missing.bin";
  e.sha256 = kHelloSha;
  e.size_bytes = kHelloSize;
  e.required = true;
  m.models.push_back(e);

  auto s = VerifyAllRequired(root.string(), m);
  EXPECT_FALSE(s.ok());
  EXPECT_EQ(s.code(), absl::StatusCode::kNotFound);
}

TEST(VerifyAllRequired, ReturnsDataLossOnShaCorruption) {
  const fs::path root = TestTmpRoot() / "verify_all_corrupt";
  fs::create_directories(root);

  constexpr char kFakeSha[] =
      "0000000000000000000000000000000000000000000000000000000000000000";
  Manifest m;
  m.schema_version = 1;
  ModelEntry e;
  e.id = "corrupt";
  e.filename = "c.bin";
  e.sha256 = kFakeSha;
  e.size_bytes = kHelloSize;
  e.required = true;
  m.models.push_back(e);

  WriteFile(root / "corrupt" / (std::string(kFakeSha) + ".bin"), kHelloContent);

  auto s = VerifyAllRequired(root.string(), m);
  EXPECT_FALSE(s.ok());
  EXPECT_EQ(s.code(), absl::StatusCode::kDataLoss);
}

TEST(VerifyAllRequired, AggregatesMultipleFailures) {
  const fs::path root = TestTmpRoot() / "verify_all_multi_fail";
  fs::create_directories(root);

  Manifest m;
  m.schema_version = 1;
  // Entry 1: missing.
  ModelEntry e1;
  e1.id = "m1";
  e1.filename = "m1.bin";
  e1.sha256 = kHelloSha;
  e1.size_bytes = kHelloSize;
  e1.required = true;
  m.models.push_back(e1);
  // Entry 2: also missing.
  ModelEntry e2;
  e2.id = "m2";
  e2.filename = "m2.bin";
  e2.sha256 = kHelloSha;
  e2.size_bytes = kHelloSize;
  e2.required = true;
  m.models.push_back(e2);

  auto s = VerifyAllRequired(root.string(), m);
  EXPECT_FALSE(s.ok());
  // Message should reference both m1 and m2.
  EXPECT_NE(std::string(s.message()).find("`m1`"), std::string::npos)
      << "message: " << s.message();
  EXPECT_NE(std::string(s.message()).find("`m2`"), std::string::npos)
      << "message: " << s.message();
}

} // namespace
} // namespace aegis::models
