# External BUILD file for @llama_cpp — llama.cpp inference runtime.
#
# ADR-0021: uses the shared ggml build (not its own bundled copy).
# LLAMA_USE_SYSTEM_GGML is not a real CMake flag in llama.cpp — instead
# we exclude the ggml subdirectory from the build and link against
# the externally-built ggml libraries.
#
# This target produces libllama.a + libcommon.a for the GGMLEmbedder
# to call llama_encode() + llama_get_embeddings_seq().

load("@rules_foreign_cc//foreign_cc:defs.bzl", "cmake")

filegroup(
    name = "all_srcs",
    srcs = glob(["**"]),
    visibility = ["//visibility:public"],
)

cmake(
    name = "llama_cpp_cmake",
    cache_entries = {
        "BUILD_SHARED_LIBS": "OFF",
        "LLAMA_BUILD_TESTS": "OFF",
        "LLAMA_BUILD_EXAMPLES": "OFF",
        "LLAMA_BUILD_SERVER": "OFF",
        "LLAMA_CURL": "OFF",
        # Use the ggml already built by @ggml — ADR-0021.
        # rules_foreign_cc makes the @ggml cmake install visible
        # via CMAKE_PREFIX_PATH in the deps mechanism below.
        "GGML_METAL": "OFF",
        "GGML_BLAS": "OFF",
        "GGML_CUDA": "OFF",
        "GGML_VULKAN": "OFF",
        "GGML_OPENMP": "OFF",
        "CMAKE_OSX_DEPLOYMENT_TARGET": "11.0",
    },
    # Depend on the ggml cmake target. rules_foreign_cc propagates
    # the install directory of deps into CMAKE_PREFIX_PATH, so
    # llama.cpp's find_package(ggml) will find our shared ggml.
    deps = ["@ggml//:ggml_cmake"],
    lib_source = ":all_srcs",
    out_static_libs = [
        "libllama.a",
    ],
    visibility = ["//visibility:public"],
)
