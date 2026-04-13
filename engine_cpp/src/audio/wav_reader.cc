// engine_cpp/src/audio/wav_reader.cc

#include "engine_cpp/src/audio/wav_reader.h"

#include <cstdint>
#include <cstring>
#include <fstream>
#include <string>

#include "absl/status/status.h"
#include "absl/strings/str_cat.h"

namespace aegis::audio {

namespace {

// Little-endian reads — WAV is always LE regardless of host byte order.
// Modern Apple Silicon + x86 are LE so memcpy is fine; this is kept
// explicit for portability.
uint32_t ReadU32LE(const char *p) {
  uint32_t v = 0;
  std::memcpy(&v, p, 4);
  return v;
}

uint16_t ReadU16LE(const char *p) {
  uint16_t v = 0;
  std::memcpy(&v, p, 2);
  return v;
}

constexpr int kExpectedSampleRate = 16000;
constexpr int kExpectedChannels = 1;
constexpr int kExpectedBitsPerSample = 16;

} // namespace

absl::StatusOr<std::vector<float>> ReadWav16kMono(const std::string &path) {
  if (path.empty()) {
    return absl::NotFoundError("ReadWav16kMono: empty path");
  }

  std::ifstream f(path, std::ios::binary);
  if (!f.is_open()) {
    return absl::NotFoundError(
        absl::StrCat("ReadWav16kMono: cannot open ", path));
  }

  // RIFF header — 12 bytes: "RIFF" | size(4) | "WAVE".
  char riff[12];
  f.read(riff, 12);
  if (f.gcount() != 12) {
    return absl::InvalidArgumentError(
        absl::StrCat("ReadWav16kMono: short RIFF header in ", path));
  }
  if (std::memcmp(riff, "RIFF", 4) != 0 ||
      std::memcmp(riff + 8, "WAVE", 4) != 0) {
    return absl::InvalidArgumentError(
        absl::StrCat("ReadWav16kMono: not a RIFF/WAVE file: ", path));
  }

  // Walk chunks until we find fmt and data.
  bool have_fmt = false;
  uint16_t audio_format = 0;
  uint16_t channels = 0;
  uint32_t sample_rate = 0;
  uint16_t bits_per_sample = 0;

  std::vector<char> pcm_raw;

  while (f.good()) {
    char hdr[8];
    f.read(hdr, 8);
    if (f.gcount() != 8)
      break; // EOF
    const uint32_t chunk_size = ReadU32LE(hdr + 4);

    if (std::memcmp(hdr, "fmt ", 4) == 0) {
      if (chunk_size < 16) {
        return absl::InvalidArgumentError(
            "ReadWav16kMono: fmt chunk too small");
      }
      std::vector<char> fmt(chunk_size);
      f.read(fmt.data(), chunk_size);
      if (static_cast<uint32_t>(f.gcount()) != chunk_size) {
        return absl::InvalidArgumentError(
            "ReadWav16kMono: truncated fmt chunk");
      }
      audio_format = ReadU16LE(fmt.data());
      channels = ReadU16LE(fmt.data() + 2);
      sample_rate = ReadU32LE(fmt.data() + 4);
      bits_per_sample = ReadU16LE(fmt.data() + 14);
      have_fmt = true;
    } else if (std::memcmp(hdr, "data", 4) == 0) {
      pcm_raw.resize(chunk_size);
      f.read(pcm_raw.data(), chunk_size);
      if (static_cast<uint32_t>(f.gcount()) != chunk_size) {
        return absl::InvalidArgumentError(
            "ReadWav16kMono: truncated data chunk");
      }
      break;
    } else {
      // Unknown chunk — seek past it (round up to even per RIFF spec).
      const uint32_t skip = chunk_size + (chunk_size & 1);
      f.seekg(skip, std::ios::cur);
    }
  }

  if (!have_fmt) {
    return absl::InvalidArgumentError(
        absl::StrCat("ReadWav16kMono: no fmt chunk in ", path));
  }
  if (pcm_raw.empty()) {
    return absl::InvalidArgumentError(
        absl::StrCat("ReadWav16kMono: no data chunk in ", path));
  }

  // Only PCM integer (format 1), mono, 16 kHz, 16-bit is accepted.
  if (audio_format != 1) {
    return absl::InvalidArgumentError(absl::StrCat(
        "ReadWav16kMono: only PCM (format 1) supported, got ", audio_format));
  }
  if (channels != kExpectedChannels) {
    return absl::InvalidArgumentError(absl::StrCat(
        "ReadWav16kMono: only mono supported, got channels=", channels));
  }
  if (sample_rate != kExpectedSampleRate) {
    return absl::InvalidArgumentError(absl::StrCat(
        "ReadWav16kMono: only 16000 Hz supported, got ", sample_rate));
  }
  if (bits_per_sample != kExpectedBitsPerSample) {
    return absl::InvalidArgumentError(absl::StrCat(
        "ReadWav16kMono: only 16-bit supported, got ", bits_per_sample));
  }

  // Convert interleaved (here: plain mono) int16 → float in [-1, 1].
  const std::size_t n_samples = pcm_raw.size() / sizeof(int16_t);
  std::vector<float> out(n_samples);
  for (std::size_t i = 0; i < n_samples; ++i) {
    int16_t s = 0;
    std::memcpy(&s, pcm_raw.data() + i * sizeof(int16_t), sizeof(int16_t));
    out[i] = static_cast<float>(s) / 32768.0f;
  }
  return out;
}

} // namespace aegis::audio
