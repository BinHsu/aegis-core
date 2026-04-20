// engine_cpp/tests/integration/stream_transcribe_test.cc
//
// Phase 1 Session 4d — full end-to-end StreamTranscribe verification.
// This is the first test that exercises the real gRPC service: the
// Aegis engine binary's handler receives a SessionStart + PcmChunk
// stream over an in-process gRPC channel, runs whisper.cpp, and
// writes TranscriptSegment messages back. If this passes, the entire
// Phase 1 pipeline (proto → grpc → Session state machine → whisper →
// fan-out) works.
//
// Uses grpc's InProcessChannel: no TCP, no sandbox egress, no
// ephemeral port; the channel lives entirely inside the test
// process. This is the canonical pattern for grpc-cpp service
// tests per the upstream cookbook.
//
// Model presence is handled with GTEST_SKIP so CI (which does not
// fetch the 75 MB model) stays green.

#include <cstdint>
#include <cstdlib>
#include <cstring>
#include <fstream>
#include <memory>
#include <mutex>
#include <string>
#include <thread>
#include <utility>
#include <vector>

#include <algorithm>

#include "absl/strings/ascii.h"
#include "engine_cpp/src/audio/wav_reader.h"
#include "engine_cpp/src/grpc/aegis_engine_service.h"
#include "engine_cpp/src/session/session_budget.h"
#include "grpcpp/grpcpp.h"
#include "proto/aegis/v1/aegis.grpc.pb.h"
#include "gtest/gtest.h"

namespace aegis::grpc_service {
namespace {

bool FileExists(const std::string &path) { return std::ifstream(path).good(); }

// Resolve the whisper-tiny-en model path, honoring the CAS layout
// (ADR-0026) that's been live since 2026-04-20. Tries CAS first, then
// falls back to the legacy flat filename so developers with old local
// layouts still skip cleanly instead of silently mis-resolving.
std::string ResolveModelPath() {
  constexpr const char *kCasRel =
      "/whisper-tiny-en/"
      "921e4cf8686fdd993dcd081a5da5b6c365bfde1162e72b08d75ac75289920b1f.bin";
  constexpr const char *kLegacyRel = "/ggml-tiny.en.bin";
  std::string dir;
  if (const char *env = std::getenv("AEGIS_MODEL_DIR"); env != nullptr) {
    dir = env;
  } else {
    dir = "models";
  }
  if (FileExists(dir + kCasRel)) {
    return dir + kCasRel;
  }
  return dir + kLegacyRel;
}

std::string ResolveWavPath() {
  if (const char *env = std::getenv("TEST_SRCDIR"); env != nullptr) {
    const std::string candidates[] = {
        std::string(env) + "/_main~_repo_rules~whisper_cpp/samples/jfk.wav",
        std::string(env) + "/+_repo_rules+whisper_cpp/samples/jfk.wav",
    };
    for (const auto &p : candidates) {
      if (FileExists(p))
        return p;
    }
  }
  return "test/golden_audio/jfk.wav";
}

// Convert float PCM [-1, 1] to int16 LE bytes as the proto expects
// (IngestMessage.PcmChunk.pcm per AudioFormat default 16-bit).
std::string FloatsToInt16LE(const std::vector<float> &samples) {
  std::string out;
  out.resize(samples.size() * sizeof(int16_t));
  for (std::size_t i = 0; i < samples.size(); ++i) {
    float f = samples[i];
    if (f > 1.0f)
      f = 1.0f;
    if (f < -1.0f)
      f = -1.0f;
    int16_t s = static_cast<int16_t>(f * 32767.0f);
    std::memcpy(out.data() + i * sizeof(int16_t), &s, sizeof(int16_t));
  }
  return out;
}

// Helper — collected egress messages, accumulated text, captured
// segment metadata. Populated by a background reader thread so the
// client's writes and the server's writes can interleave. Pre–live-
// windowing, the server wrote zero egress until WritesDone; a single-
// threaded test that wrote all PCM first and read all egress after was
// safe. With live windowing the server writes mid-stream — if the
// client didn't start reading, the in-process gRPC channel's bounded
// buffers filled and both sides deadlocked indefinitely. A background
// reader is the minimal correct pattern for any bidi stream test that
// crosses the "server writes during client writes" boundary.
struct EgressCollection {
  std::string all_text;
  std::vector<std::uint64_t> segment_ids;
  std::vector<std::int64_t> segment_spans_ms;
  std::mutex mu;
};

std::thread
SpawnEgressReader(::grpc::ClientReaderWriter<aegis::v1::IngestMessage,
                                             aegis::v1::EgressMessage> *stream,
                  EgressCollection *out) {
  return std::thread([stream, out]() {
    aegis::v1::EgressMessage egress;
    while (stream->Read(&egress)) {
      if (!egress.has_transcript())
        continue;
      const auto &t = egress.transcript();
      std::lock_guard<std::mutex> lock(out->mu);
      out->all_text += t.text();
      out->segment_ids.push_back(t.segment_id());
      out->segment_spans_ms.push_back(t.end_ms() - t.start_ms());
    }
  });
}

TEST(StreamTranscribeTest, JfkEndToEnd) {
  const std::string model_path = ResolveModelPath();
  if (!FileExists(model_path)) {
    GTEST_SKIP() << "model not present at " << model_path
                 << ". Run ./tools/scripts/download_models.sh --all";
  }
  const std::string wav_path = ResolveWavPath();
  if (!FileExists(wav_path)) {
    GTEST_SKIP() << "audio fixture not present at " << wav_path;
  }

  // Set up in-process gRPC server with our real service.
  session::SessionBudget budget(1024ULL * 1024 * 1024); // 1 GiB test cap
  AegisEngineServiceImpl service(&budget, model_path);

  ::grpc::ServerBuilder builder;
  builder.RegisterService(&service);
  std::unique_ptr<::grpc::Server> server(builder.BuildAndStart());
  ASSERT_TRUE(server);

  auto channel = server->InProcessChannel(::grpc::ChannelArguments());
  auto stub = aegis::v1::Engine::NewStub(channel);

  ::grpc::ClientContext ctx;
  auto stream = stub->StreamTranscribe(&ctx);

  // Kick off background reader BEFORE writing — see EgressCollection doc.
  EgressCollection collected;
  std::thread reader = SpawnEgressReader(stream.get(), &collected);

  // Send SessionStart first.
  {
    aegis::v1::IngestMessage msg;
    auto *start = msg.mutable_session_start();
    start->set_session_id("test-4d");
    start->set_tenant_id(""); // Local-mode-style empty tenant
    start->set_rag_id("test-rag");
    start->set_estimated_bytes(200ULL * 1024 * 1024);
    auto *fmt = start->mutable_audio_format();
    fmt->set_sample_rate_hz(16000);
    fmt->set_channels(1);
    fmt->set_bits_per_sample(16);
    start->add_language_hints("en");
    ASSERT_TRUE(stream->Write(msg));
  }

  // Load WAV and convert to int16 LE PCM.
  auto samples_or = audio::ReadWav16kMono(wav_path);
  ASSERT_TRUE(samples_or.ok()) << samples_or.status();
  const std::string int16_le = FloatsToInt16LE(*samples_or);

  // Split into ~32 KB chunks to exercise the stream (not just one big write).
  constexpr std::size_t kChunkBytes = 32 * 1024;
  std::uint64_t chunk_id = 0;
  for (std::size_t off = 0; off < int16_le.size(); off += kChunkBytes) {
    const std::size_t n = std::min(kChunkBytes, int16_le.size() - off);
    aegis::v1::IngestMessage msg;
    auto *pcm = msg.mutable_pcm();
    pcm->set_pcm(int16_le.data() + off, n);
    pcm->set_chunk_id(chunk_id++);
    pcm->set_offset_ms(0);
    ASSERT_TRUE(stream->Write(msg));
  }

  // Signal end.
  {
    aegis::v1::IngestMessage msg;
    msg.mutable_control()->set_kind(aegis::v1::CONTROL_KIND_END_STREAM);
    ASSERT_TRUE(stream->Write(msg));
  }
  stream->WritesDone();

  reader.join(); // Drains until server closes Read loop.
  const ::grpc::Status status = stream->Finish();
  EXPECT_TRUE(status.ok()) << "grpc status: " << status.error_message();

  // Budget must be fully released after session end.
  EXPECT_EQ(budget.BytesUsed(), 0u);

  // Content check — JFK's opening line, lowercased for tokenizer
  // robustness. With live windowing the text is concatenated across
  // per-window transcriptions; word boundaries may end up glued
  // ("askNot"). We lowercase AND strip spaces before substring search
  // so the assertion is robust to cut-at-boundary glue.
  auto lowerNoSpace = [](std::string s) {
    s = absl::AsciiStrToLower(s);
    s.erase(std::remove(s.begin(), s.end(), ' '), s.end());
    return s;
  };
  const std::string needle_1 = lowerNoSpace("ask not");
  const std::string needle_2 = lowerNoSpace("your country");
  const std::string hay = lowerNoSpace(collected.all_text);
  EXPECT_NE(hay.find(needle_1), std::string::npos)
      << "transcript did not contain 'ask not'; got: " << collected.all_text;
  EXPECT_NE(hay.find(needle_2), std::string::npos)
      << "transcript did not contain 'your country'; got: "
      << collected.all_text;

  server->Shutdown();
}

TEST(StreamTranscribeTest, LiveWindowEmitsMidStream) {
  // Regression for Incident 14 (2026-04-20). Pre-fix `Session::Run`
  // accumulated PCM for the entire stream and emitted a single
  // transcript segment on final flush (Stage 5). A live listener saw
  // nothing for the whole session — the "Live transcript" UI panel
  // stayed at "Waiting for the first segment…" forever.
  //
  // This test asserts the live-windowing contract post-fix:
  //   - Audio longer than one window (3 s) MUST produce at least one
  //     transcript segment BEFORE the stream closes.
  //   - Subsequent windows MUST emit additional segments — total
  //     segments > 1 for a multi-window session (not a single batch).
  //   - segment_id is a monotonic session-local counter starting at 1.
  //   - start_ms < end_ms for every segment (timestamps are derived
  //     from sample offsets, not hardcoded 0).
  //
  // Uses the background-reader pattern (see SpawnEgressReader) —
  // under live windowing the server writes egress during the client's
  // PCM-write phase, so a single-threaded client deadlocks on the
  // in-process channel's bounded buffers.
  //
  // Gated on model presence (like JfkEndToEnd above) so CI without
  // the 75 MB model stays green.
  const std::string model_path = ResolveModelPath();
  if (!FileExists(model_path)) {
    GTEST_SKIP() << "model not present at " << model_path
                 << ". Run ./tools/scripts/download_models.sh --all";
  }
  const std::string wav_path = ResolveWavPath();
  if (!FileExists(wav_path)) {
    GTEST_SKIP() << "audio fixture not present at " << wav_path;
  }

  auto samples_or = audio::ReadWav16kMono(wav_path);
  ASSERT_TRUE(samples_or.ok()) << samples_or.status();
  const std::vector<float> &samples = *samples_or;
  // Require enough audio to span at least two 3-second windows.
  ASSERT_GE(samples.size(), static_cast<std::size_t>(6 * 16000))
      << "fixture too short for multi-window regression; need ≥6s, got "
      << (samples.size() / 16000) << "s";
  const std::string int16_le = FloatsToInt16LE(samples);

  session::SessionBudget budget(1024ULL * 1024 * 1024);
  AegisEngineServiceImpl service(&budget, model_path);

  ::grpc::ServerBuilder builder;
  builder.RegisterService(&service);
  std::unique_ptr<::grpc::Server> server(builder.BuildAndStart());
  ASSERT_TRUE(server);

  auto channel = server->InProcessChannel(::grpc::ChannelArguments());
  auto stub = aegis::v1::Engine::NewStub(channel);

  ::grpc::ClientContext ctx;
  auto stream = stub->StreamTranscribe(&ctx);

  EgressCollection collected;
  std::thread reader = SpawnEgressReader(stream.get(), &collected);

  {
    aegis::v1::IngestMessage msg;
    auto *start = msg.mutable_session_start();
    start->set_session_id("test-live-window");
    auto *fmt = start->mutable_audio_format();
    fmt->set_sample_rate_hz(16000);
    fmt->set_channels(1);
    fmt->set_bits_per_sample(16);
    ASSERT_TRUE(stream->Write(msg));
  }

  // Chunk strategy: split PCM into ~32 KB PcmChunks (same as
  // JfkEndToEnd). With 6+ seconds of audio at 32 KB per chunk
  // (= 16384 samples = ~1.02 s), we feed at least 6 chunks —
  // which triggers at least 2 window flushes at 3-second boundaries.
  constexpr std::size_t kChunkBytes = 32 * 1024;
  std::uint64_t chunk_id = 0;
  for (std::size_t off = 0; off < int16_le.size(); off += kChunkBytes) {
    const std::size_t n = std::min(kChunkBytes, int16_le.size() - off);
    aegis::v1::IngestMessage msg;
    auto *pcm = msg.mutable_pcm();
    pcm->set_pcm(int16_le.data() + off, n);
    pcm->set_chunk_id(chunk_id++);
    pcm->set_offset_ms(0);
    ASSERT_TRUE(stream->Write(msg));
  }

  {
    aegis::v1::IngestMessage msg;
    msg.mutable_control()->set_kind(aegis::v1::CONTROL_KIND_END_STREAM);
    ASSERT_TRUE(stream->Write(msg));
  }
  stream->WritesDone();
  reader.join();

  const ::grpc::Status status = stream->Finish();
  EXPECT_TRUE(status.ok()) << "grpc status: " << status.error_message();

  // Core regression assertions for Incident 14:
  std::lock_guard<std::mutex> lock(collected.mu);

  // 1) Multiple segments — batch-mode would have produced exactly 1.
  EXPECT_GT(collected.segment_ids.size(), 1u)
      << "live windowing regressed: only " << collected.segment_ids.size()
      << " segment(s) emitted; pre-fix Session::Run emitted exactly 1 at "
         "stream end. A working live windowing pipeline emits multiple "
         "segments over a >6s audio stream.";

  // 2) segment_id is monotonic starting from 1.
  for (std::size_t i = 0; i < collected.segment_ids.size(); ++i) {
    EXPECT_EQ(collected.segment_ids[i], static_cast<std::uint64_t>(i + 1))
        << "segment_id not monotonic at index " << i;
  }

  // 3) Each segment carries a non-trivial time span (start_ms < end_ms).
  for (std::size_t i = 0; i < collected.segment_spans_ms.size(); ++i) {
    EXPECT_GT(collected.segment_spans_ms[i], 0)
        << "segment " << (i + 1)
        << " has zero/negative time span — start_ms "
           "and end_ms must be derived from sample offsets, not hardcoded 0";
  }

  EXPECT_EQ(budget.BytesUsed(), 0u);
  server->Shutdown();
}

TEST(StreamTranscribeTest, FirstMessageMustBeSessionStart) {
  session::SessionBudget budget(1024ULL * 1024 * 1024);
  AegisEngineServiceImpl service(&budget, "/no/such/model"); // unused

  ::grpc::ServerBuilder builder;
  builder.RegisterService(&service);
  std::unique_ptr<::grpc::Server> server(builder.BuildAndStart());
  ASSERT_TRUE(server);

  auto stub = aegis::v1::Engine::NewStub(
      server->InProcessChannel(::grpc::ChannelArguments()));

  ::grpc::ClientContext ctx;
  auto stream = stub->StreamTranscribe(&ctx);

  // Skip SessionStart, send a ControlEvent first — must be rejected.
  aegis::v1::IngestMessage msg;
  msg.mutable_control()->set_kind(aegis::v1::CONTROL_KIND_PAUSE);
  // Write may or may not succeed depending on when the server closes; either
  // way, Finish() below should surface an error status.
  (void)stream->Write(msg);
  stream->WritesDone();

  aegis::v1::EgressMessage egress;
  while (stream->Read(&egress)) {
  }
  const ::grpc::Status status = stream->Finish();
  EXPECT_FALSE(status.ok());
  EXPECT_EQ(status.error_code(), ::grpc::StatusCode::INVALID_ARGUMENT);

  // No budget should have been reserved on the error path.
  EXPECT_EQ(budget.BytesUsed(), 0u);

  server->Shutdown();
}

} // namespace
} // namespace aegis::grpc_service
