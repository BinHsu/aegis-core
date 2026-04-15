# Upstream-facing BUILD for libopus (http_archive uses this via
# MODULE.bazel's `build_file` attribute). `rules_foreign_cc`'s
# `cmake` rule drives a real CMake configure + build using the
# hermetic clang toolchain — same pattern as whisper.cpp in this
# repo (`engine_cpp/third_party/whisper_cpp/whisper_cpp.BUILD`).
#
# We build the static library only. `opus` is small (~400 KB static
# on darwin-arm64) — vs a dynamic .dylib, static linking removes a
# runtime dependency surface and keeps the engine container image
# trivially relocatable.

load("@rules_foreign_cc//foreign_cc:defs.bzl", "cmake")

filegroup(
    name = "all_srcs",
    srcs = glob(["**"]),
    visibility = ["//visibility:public"],
)

cmake(
    name = "libopus",
    lib_source = ":all_srcs",
    # CMake cache flags — upstream `OPUS_BUILD_SHARED_LIBRARY` defaults
    # off (good; static is what we want). Explicitly disable the
    # testing / fuzzing / programs targets because they aren't linked
    # into our engine and they add ~30 s of cold-build time. OPUS_X86_MAY_HAVE_*
    # flags are auto-detected but we pin conservative-safe defaults so
    # the resulting .a is identical across dev machines and CI.
    cache_entries = {
        "BUILD_SHARED_LIBS": "OFF",
        "OPUS_BUILD_TESTING": "OFF",
        "OPUS_BUILD_PROGRAMS": "OFF",
        "OPUS_INSTALL_PKG_CONFIG_MODULE": "OFF",
        "OPUS_INSTALL_CMAKE_CONFIG_MODULE": "OFF",
    },
    out_static_libs = ["libopus.a"],
    visibility = ["//visibility:public"],
)
