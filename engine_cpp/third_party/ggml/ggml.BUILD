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
        # Portable CPU baseline, NOT -mcpu=native. ggml defaults GGML_NATIVE=ON,
        # which bakes in the BUILD HOST's CPU features. The engine is built on a
        # GitHub arm64 runner (Ampere/Graviton) but runs on Apple Silicon via
        # apple/container — a different ARM microarch, so a native build SIGILLs
        # at the first unsupported instruction during model load (exit 132;
        # caught WS2-2 live verify 2026-06-16). OFF compiles an armv8-a baseline
        # that runs on any arm64 (Apple Silicon is a superset). If a perf
        # baseline ever needs per-host tuning, build per-target — don't ship one
        # image to heterogeneous CPUs.
        "GGML_NATIVE": "OFF",
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
