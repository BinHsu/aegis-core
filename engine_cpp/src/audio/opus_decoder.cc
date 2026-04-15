// engine_cpp/src/audio/opus_decoder.cc

#include "engine_cpp/src/audio/opus_decoder.h"

#include <cstddef>
#include <memory>
#include <span>
#include <vector>

#include "absl/status/status.h"
#include "absl/status/statusor.h"
#include "absl/strings/str_cat.h"
#include "opus/opus.h" // from @libopus

namespace aegis::audio {

absl::StatusOr<std::unique_ptr<OpusDecoder>> OpusDecoder::Create() {
  int err = 0;
  ::OpusDecoder *dec =
      ::opus_decoder_create(kOutputSampleRateHz, kOutputChannels, &err);
  if (err != OPUS_OK || dec == nullptr) {
    return absl::InvalidArgumentError(absl::StrCat(
        "OpusDecoder::Create: opus_decoder_create failed, err=", err));
  }
  return std::unique_ptr<OpusDecoder>(new OpusDecoder(dec));
}

OpusDecoder::OpusDecoder(::OpusDecoder *dec) : dec_(dec) {}

OpusDecoder::~OpusDecoder() {
  if (dec_ != nullptr) {
    ::opus_decoder_destroy(dec_);
    dec_ = nullptr;
  }
}

absl::StatusOr<std::vector<float>>
OpusDecoder::Decode(std::span<const std::byte> opus_payload) {
  if (opus_payload.empty()) {
    return absl::InvalidArgumentError(
        "OpusDecoder::Decode: empty payload "
        "(null-input PLC convention is unsupported; Gateway drops "
        "empty RTP payloads before forwarding)");
  }

  // Pre-size for the worst case (120 ms at 16 kHz mono). opus_decode_float
  // writes `rc` samples into the buffer; we resize down at the end so the
  // returned vector's size reflects the actual decoded length.
  std::vector<float> out(static_cast<std::size_t>(kMaxSamplesPerFrame) *
                         kOutputChannels);

  const int rc = ::opus_decode_float(
      dec_, reinterpret_cast<const unsigned char *>(opus_payload.data()),
      static_cast<opus_int32>(opus_payload.size()), out.data(),
      kMaxSamplesPerFrame, /*decode_fec=*/0);
  if (rc < 0) {
    return absl::InvalidArgumentError(
        absl::StrCat("OpusDecoder::Decode: opus_decode_float rc=", rc));
  }

  out.resize(static_cast<std::size_t>(rc) * kOutputChannels);
  return out;
}

} // namespace aegis::audio
