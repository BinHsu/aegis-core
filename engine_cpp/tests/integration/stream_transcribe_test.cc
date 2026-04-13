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
#include <string>
#include <utility>
#include <vector>

#include "absl/strings/ascii.h"
#include "engine_cpp/src/audio/wav_reader.h"
#include "engine_cpp/src/grpc/aegis_engine_service.h"
#include "engine_cpp/src/session/resource_budget.h"
#include "grpcpp/grpcpp.h"
#include "proto/aegis/v1/aegis.grpc.pb.h"
#include "gtest/gtest.h"

namespace aegis::grpc_service {
namespace {

bool FileExists(const std::string &path) { return std::ifstream(path).good(); }

std::string ResolveModelPath() {
  if (const char *env = std::getenv("AEGIS_MODEL_DIR"); env != nullptr) {
    return std::string(env) + "/ggml-tiny.en.bin";
  }
  return "models/ggml-tiny.en.bin";
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
  session::ResourceBudget budget(1024ULL * 1024 * 1024); // 1 GiB test cap
  AegisEngineServiceImpl service(&budget, model_path);

  ::grpc::ServerBuilder builder;
  builder.RegisterService(&service);
  std::unique_ptr<::grpc::Server> server(builder.BuildAndStart());
  ASSERT_TRUE(server);

  auto channel = server->InProcessChannel(::grpc::ChannelArguments());
  auto stub = aegis::v1::Engine::NewStub(channel);

  ::grpc::ClientContext ctx;
  auto stream = stub->StreamTranscribe(&ctx);

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
  uint64_t chunk_id = 0;
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

  // Collect all transcript segments emitted.
  std::string all_text;
  aegis::v1::EgressMessage egress;
  while (stream->Read(&egress)) {
    if (egress.has_transcript()) {
      all_text += egress.transcript().text();
    }
  }
  const ::grpc::Status status = stream->Finish();
  EXPECT_TRUE(status.ok()) << "grpc status: " << status.error_message();

  // Budget must be fully released after session end.
  EXPECT_EQ(budget.BytesUsed(), 0u);

  // Content check — JFK's opening line, lowercased for tokenizer robustness.
  const std::string lower = absl::AsciiStrToLower(all_text);
  EXPECT_NE(lower.find("ask not"), std::string::npos)
      << "transcript did not contain 'ask not'; got: " << all_text;
  EXPECT_NE(lower.find("your country"), std::string::npos)
      << "transcript did not contain 'your country'; got: " << all_text;

  server->Shutdown();
}

TEST(StreamTranscribeTest, FirstMessageMustBeSessionStart) {
  session::ResourceBudget budget(1024ULL * 1024 * 1024);
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
