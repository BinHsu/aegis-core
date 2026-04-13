// engine_cpp/src/audio/wav_reader.h
//
// Minimal WAV parser: accepts only the subset that whisper.cpp consumes
// — 16-bit PCM, mono, 16 kHz. Anything else is an InvalidArgumentError.
// Returns samples as std::vector<float> normalized to [-1.0, 1.0], which
// is whisper_full's required input format.
//
// This exists primarily for test fixtures (Session 4c integration test
// against samples/jfk.wav). Production audio path receives raw int16
// PCM over the gRPC IngestMessage.pcm field and does not go through
// this parser.

#ifndef AEGIS_ENGINE_CPP_SRC_AUDIO_WAV_READER_H_
#define AEGIS_ENGINE_CPP_SRC_AUDIO_WAV_READER_H_

#include <string>
#include <vector>

#include "absl/status/statusor.h"

namespace aegis::audio {

// Read a 16-bit PCM, mono, 16 kHz WAV file and return the samples as
// floats in [-1.0, 1.0]. Errors:
//   NotFoundError        — path is empty or file unreadable.
//   InvalidArgumentError — not RIFF/WAVE, wrong format/channels/rate,
//                          truncated header, or missing data chunk.
absl::StatusOr<std::vector<float>> ReadWav16kMono(const std::string &path);

} // namespace aegis::audio

#endif // AEGIS_ENGINE_CPP_SRC_AUDIO_WAV_READER_H_
