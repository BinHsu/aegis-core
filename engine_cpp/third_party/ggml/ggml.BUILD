# External BUILD file for @ggml — standalone ggml runtime.
#
# ADR-0021: one ggml build, consumed by both whisper.cpp and llama.cpp
# via their USE_SYSTEM_GGML CMake flags. This target produces the
# static libraries and cmake config that both consumers find_package().
#
# CPU-only baseline (Phase 1). Metal/CUDA backends are OFF — same
# policy as the whisper_cpp.BUILD (see those comments). Phase 2+
# flips backends per --config=metal|cuda.

load("@rules_foreign_cc//foreign_cc:defs.bzl", "cmake")

filegroup(
    name = "all_srcs",
    srcs = glob(["**"]),
    visibility = ["//visibility:public"],
)

cmake(
    name = "ggml_cmake",
    cache_entries = {
        "BUILD_SHARED_LIBS": "OFF",
        "GGML_BUILD_TESTS": "OFF",
        "GGML_BUILD_EXAMPLES": "OFF",
        "GGML_METAL": "OFF",
        "GGML_BLAS": "OFF",
        "GGML_CUDA": "OFF",
        "GGML_VULKAN": "OFF",
        "GGML_OPENMP": "OFF",
        "CMAKE_OSX_DEPLOYMENT_TARGET": "11.0",
    },
    lib_source = ":all_srcs",
    out_static_libs = [
        "libggml.a",
        "libggml-base.a",
        "libggml-cpu.a",
    ],
    visibility = ["//visibility:public"],
)
