// engine_cpp/tests/unit/opus_decoder_test.cc
//
// Roundtrip test: encode a known waveform with libopus's encoder, then
// decode it with our OpusDecoder and verify sample count + non-zero
// energy. ADR-0016 checklist mentioned a checked-in fixture under
// engine_cpp/testdata/opus/, but encoding in-test is cleaner here —
// libopus is already linked so the encoder symbols cost nothing,
// and we avoid binary fixtures whose bytes drift with libopus version
// bumps. Deviation noted in the commit message.

#include "engine_cpp/src/audio/opus_decoder.h"

#include <cmath>
#include <cstddef>
#include <span>
#include <vector>

#include "absl/status/status.h"
#include "opus/opus.h" // from @libopus, encoder side only
#include "gtest/gtest.h"

namespace aegis::audio {
namespace {

// 20 ms at 16 kHz mono = 320 samples — one of Opus's standard frame
// sizes (2.5 / 5 / 10 / 20 / 40 / 60 ms).
constexpr int kFrameSamples = 320;
constexpr int kSampleRateHz = 16000;
constexpr int kChannels = 1;
constexpr double kTwoPi = 6.283185307179586;

std::vector<float> SineWave() {
  std::vector<float> pcm(kFrameSamples);
  constexpr double kFreqHz = 1000.0;
  for (int i = 0; i < kFrameSamples; ++i) {
    pcm[i] = 0.5f *
             static_cast<float>(std::sin(kTwoPi * kFreqHz * i / kSampleRateHz));
  }
  return pcm;
}

std::vector<std::byte> EncodeFrame(const std::vector<float> &pcm) {
  int err = 0;
  ::OpusEncoder *enc = ::opus_encoder_create(kSampleRateHz, kChannels,
                                             OPUS_APPLICATION_VOIP, &err);
  EXPECT_EQ(err, OPUS_OK);
  EXPECT_NE(enc, nullptr);

  constexpr int kMaxPacket = 1275; // RFC 6716 §3.2
  std::vector<unsigned char> buf(kMaxPacket);
  const int n = ::opus_encode_float(enc, pcm.data(), kFrameSamples, buf.data(),
                                    kMaxPacket);
  EXPECT_GT(n, 0);
  ::opus_encoder_destroy(enc);

  std::vector<std::byte> out(static_cast<std::size_t>(n));
  for (int i = 0; i < n; ++i) {
    out[i] = static_cast<std::byte>(buf[i]);
  }
  return out;
}

TEST(OpusDecoderTest, CreateSucceeds) {
  auto decoder = OpusDecoder::Create();
  ASSERT_TRUE(decoder.ok()) << decoder.status();
  EXPECT_NE(decoder->get(), nullptr);
}

TEST(OpusDecoderTest, DecodeRejectsEmptyPayload) {
  auto decoder = OpusDecoder::Create();
  ASSERT_TRUE(decoder.ok());
  const auto result = (*decoder)->Decode({});
  ASSERT_FALSE(result.ok());
  EXPECT_EQ(result.status().code(), absl::StatusCode::kInvalidArgument);
}

TEST(OpusDecoderTest, RoundtripSineWaveYields20msOfSamples) {
  const std::vector<float> pcm_in = SineWave();
  const std::vector<std::byte> encoded = EncodeFrame(pcm_in);
  EXPECT_GT(encoded.size(), 0u);
  EXPECT_LT(encoded.size(), 200u); // VoIP-app single tone stays small

  auto decoder = OpusDecoder::Create();
  ASSERT_TRUE(decoder.ok());
  auto pcm_out = (*decoder)->Decode(encoded);
  ASSERT_TRUE(pcm_out.ok()) << pcm_out.status();

  EXPECT_EQ(pcm_out->size(),
            static_cast<std::size_t>(kFrameSamples * kChannels));

  // Lossy codec → not bit-identical, but sine energy must survive. A
  // true silence frame would decode to ~0 energy; 1 kHz sine at 0.5
  // amplitude has theoretical sum-of-squares ≈ 320 * 0.125 = 40.
  double sum_sq = 0.0;
  for (float s : *pcm_out) {
    sum_sq += static_cast<double>(s) * s;
  }
  EXPECT_GT(sum_sq, 1.0);
}

} // namespace
} // namespace aegis::audio
