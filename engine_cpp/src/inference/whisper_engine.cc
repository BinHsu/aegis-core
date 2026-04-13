// engine_cpp/src/inference/whisper_engine.cc

#include "engine_cpp/src/inference/whisper_engine.h"

#include <sys/stat.h>

#include "absl/strings/str_cat.h"
#include "whisper.h" // from @whisper_cpp

namespace aegis::inference {

namespace {

bool FileExists(const std::string &path) {
  struct stat st {};
  return !path.empty() && ::stat(path.c_str(), &st) == 0 && S_ISREG(st.st_mode);
}

} // namespace

std::string WhisperSystemInfo() {
  const char *info = whisper_print_system_info();
  return info ? std::string(info) : std::string();
}

absl::StatusOr<std::unique_ptr<WhisperEngine>>
WhisperEngine::Create(const std::string &model_path) {
  if (!FileExists(model_path)) {
    return absl::NotFoundError(
        absl::StrCat("WhisperEngine: model file not found: ", model_path));
  }

  whisper_context_params params = whisper_context_default_params();
  // Session 4b runs on CPU backend only (ADR-0009 Sub-decision 4 default).
  // Session 4c+ will thread --config=metal|cuda through to these params.
  params.use_gpu = false;
  params.flash_attn = false;

  whisper_context *ctx =
      whisper_init_from_file_with_params(model_path.c_str(), params);
  if (ctx == nullptr) {
    return absl::InternalError(absl::StrCat(
        "WhisperEngine: whisper_init_from_file_with_params returned null for ",
        model_path));
  }

  // Can't use std::make_unique with a private ctor, so wrap manually.
  return std::unique_ptr<WhisperEngine>(new WhisperEngine(ctx));
}

WhisperEngine::WhisperEngine(whisper_context *ctx) noexcept : ctx_(ctx) {}

WhisperEngine::~WhisperEngine() {
  if (ctx_ != nullptr) {
    whisper_free(ctx_);
    ctx_ = nullptr;
  }
}

absl::StatusOr<std::string>
WhisperEngine::Transcribe(std::span<const float> samples) {
  if (samples.empty()) {
    return absl::InvalidArgumentError(
        "WhisperEngine::Transcribe: samples is empty");
  }

  whisper_full_params params =
      whisper_full_default_params(WHISPER_SAMPLING_GREEDY);
  // Session 4b defaults — real session config (language hint, n_threads,
  // initial prompt, etc.) lands in Session 4c per IngestMessage.SessionStart.
  params.print_progress = false;
  params.print_special = false;
  params.print_realtime = false;
  params.print_timestamps = false;
  params.translate = false;
  params.single_segment = false;
  params.n_threads = 4;

  const int rc = whisper_full(ctx_, params, samples.data(),
                              static_cast<int>(samples.size()));
  if (rc != 0) {
    return absl::InternalError(
        absl::StrCat("WhisperEngine::Transcribe: whisper_full rc=", rc));
  }

  std::string text;
  const int n = whisper_full_n_segments(ctx_);
  for (int i = 0; i < n; ++i) {
    const char *seg = whisper_full_get_segment_text(ctx_, i);
    if (seg != nullptr) {
      text.append(seg);
    }
  }
  return text;
}

} // namespace aegis::inference
