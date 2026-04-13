// engine_cpp/src/inference/whisper_engine.cc

#include "engine_cpp/src/inference/whisper_engine.h"

#include "whisper.h" // from @whisper_cpp

namespace aegis::inference {

std::string WhisperSystemInfo() {
  // whisper_print_system_info returns a `const char*` owned by whisper's
  // static storage. Copy into std::string so callers don't depend on
  // that lifetime detail.
  const char *info = whisper_print_system_info();
  return info ? std::string(info) : std::string();
}

} // namespace aegis::inference
