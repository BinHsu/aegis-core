# External BUILD file for @whisper_cpp, fetched via http_archive in
# MODULE.bazel. Per ADR-0009 Sub-decision 3 we use rules_foreign_cc's
# cmake rule; upstream whisper.cpp moved from Makefile to CMake.
#
# This file lives in the HTTP_ARCHIVE repo context, which does NOT see
# @platforms, @aegis_core, or other repos declared by the root module.
# So we only emit the low-level cmake target here; the consumer-facing
# wrapper (with macOS framework linkopts) lives in the root module at
# //engine_cpp/third_party/whisper_cpp:whisper_cpp.

load("@rules_foreign_cc//foreign_cc:defs.bzl", "cmake")

filegroup(
    name = "all_srcs",
    srcs = glob(["**"]),
    visibility = ["//visibility:public"],
)

# Audio fixtures that come bundled with the upstream whisper.cpp tarball.
# Session 4c integration test transcribes jfk.wav (11 seconds of JFK's
# inaugural address) and asserts the output contains known phrases.
filegroup(
    name = "samples",
    srcs = glob(["samples/*.wav"]),
    visibility = ["//visibility:public"],
)

cmake(
    name = "whisper_cpp_cmake",
    cache_entries = {
        "BUILD_SHARED_LIBS": "OFF",
        "WHISPER_BUILD_TESTS": "OFF",
        "WHISPER_BUILD_EXAMPLES": "OFF",
        "WHISPER_BUILD_SERVER": "OFF",
        # Session 4a is CPU-only. ggml auto-enables GGML_METAL and GGML_BLAS
        # on macOS by default, which makes ggml-backend-reg.cpp reference
        # _ggml_backend_metal_reg / _ggml_backend_blas_reg — symbols that
        # live in libggml-metal.a / libggml-blas.a which we don't include
        # in out_static_libs. Explicitly OFF here; Session 4b+ flips them
        # per --config=metal|cuda via a backend-aware wrapper.
        "GGML_METAL": "OFF",
        "GGML_BLAS": "OFF",
        "GGML_CUDA": "OFF",
        "GGML_VULKAN": "OFF",
    },
    # Backend selection (metal/cuda flags) is deferred to the consumer
    # wrapper in the root module, which has access to
    # @aegis_core//engine_cpp:backend_* config_setting targets.
    lib_source = ":all_srcs",
    out_static_libs = [
        "libwhisper.a",
        "libggml.a",
        "libggml-base.a",
        "libggml-cpu.a",
    ],
    visibility = ["//visibility:public"],
)
