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
        # ggml enables OpenMP by default on Linux, which introduces a
        # link dependency on libgomp (GOMP_barrier / GOMP_parallel /
        # omp_get_thread_num / etc.). Our CI runners' default toolchain
        # links with GCC but without -lgomp in linkopts; the Linux
        # build then fails at engine-binary link time. macOS builds are
        # unaffected because Apple Clang ships OpenMP support via the
        # runtime by default.
        #
        # Disable OpenMP in ggml so it falls back to pthreads for
        # threading. Small perf cost at Phase-1 (CPU-only baseline)
        # scale; revisit when Phase 2+ load tests show threading as a
        # bottleneck, and add -lgomp to Linux linkopts at that point.
        "GGML_OPENMP": "OFF",
        # Pin to match Bazel's apple toolchain default (11.0). Without
        # this, CMake picks up the host SDK (currently darwin 26.3) and
        # ld prints ~18 warnings per engine binary link about the
        # newer-than-link-target libggml-cpu object files. Same root
        # cause and fix as libopus (commit 51835b1); see incident 09's
        # Prevention section. Safe to keep aligned with whatever
        # MACOSX_DEPLOYMENT_TARGET Bazel's apple toolchain emits.
        "CMAKE_OSX_DEPLOYMENT_TARGET": "11.0",
    },
    # Backend selection (metal/cuda flags) is deferred to the consumer
    # wrapper in the root module, which has access to
    # @aegis_core//engine_cpp:backend_* config_setting targets.
    # Use the shared ggml build (ADR-0021) instead of the bundled copy.
    # rules_foreign_cc propagates @ggml's install dir into CMAKE_PREFIX_PATH
    # so whisper.cpp's add_subdirectory(ggml) finds the pre-built libraries.
    deps = ["@ggml//:ggml_cmake"],
    lib_source = ":all_srcs",
    out_static_libs = [
        "libwhisper.a",
    ],
    visibility = ["//visibility:public"],
)
