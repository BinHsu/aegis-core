#!/usr/bin/env python3
# tools/rag/seed.py
#
# Skeleton for the RAG corpus → Qdrant index pipeline. Per ADR-0019,
# the corpus lives in git at docs/rag/taiwan.md; the vector index
# does NOT live in git — this script is how a reviewer / CI / cloud
# deploy rebuilds it from source.
#
# Setup (once, per CLAUDE.md Rule 6 — venv lives inside the repo):
#
#     python3 -m venv .venv
#     source .venv/bin/activate
#     pip install -r tools/rag/requirements.txt
#
# Run (local Qdrant in-memory, smoke-test):
#
#     python tools/rag/seed.py --target=local
#
# Run (cloud Qdrant, CI deploy):
#
#     QDRANT_URL=... QDRANT_API_KEY=... \
#         python tools/rag/seed.py --target=cloud
#
# NOTE: this is a SKELETON. Corpus file doesn't exist yet; the actual
# bge-m3 load and Qdrant upsert calls are present but not exercised
# end-to-end in this commit. See ADR-0019 implementation checklist.

from __future__ import annotations

import argparse
import hashlib
import os
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Iterable


# --- Paths ------------------------------------------------------------

REPO_ROOT = Path(__file__).resolve().parents[2]
CORPUS_PATH = REPO_ROOT / "docs" / "rag" / "taiwan.md"
COLLECTION_NAME = "aegis_taiwan_zh_tw"


# --- Chunking --------------------------------------------------------
# Chinese-aware separators per ADR-0019 §Decision.3. Order matters:
# the splitter tries each separator in turn, falling back to the
# finer-grained ones only when a chunk still exceeds CHUNK_SIZE.

CHUNK_SIZE = 450
CHUNK_OVERLAP = 80
SEPARATORS = ["\n\n", "\n", "。", "！", "？", "，", " ", ""]


@dataclass(frozen=True)
class Chunk:
    chunk_id: str  # sha256(corpus_id || index) prefix, stable across reruns
    text: str
    offset: int  # byte offset into the corpus, for traceability


def chunk_text(text: str) -> list[Chunk]:
    """Recursive character splitter with Chinese-aware separators.

    Real implementation delegates to langchain's RecursiveCharacterTextSplitter
    with separators=SEPARATORS, chunk_size=CHUNK_SIZE, chunk_overlap=CHUNK_OVERLAP.
    Skeleton version below emits one chunk per paragraph so the shape of
    downstream code is callable without langchain installed.
    """
    chunks: list[Chunk] = []
    offset = 0
    for i, para in enumerate(text.split("\n\n")):
        stripped = para.strip()
        if not stripped:
            offset += len(para) + 2
            continue
        chunk_id = hashlib.sha256(f"{i}:{stripped[:32]}".encode("utf-8")).hexdigest()[:16]
        chunks.append(Chunk(chunk_id=chunk_id, text=stripped, offset=offset))
        offset += len(para) + 2
    return chunks


# --- Embeddings -------------------------------------------------------

def load_embedder():
    """Load bge-m3 via FlagEmbedding. First run downloads ~2.2 GB of
    weights to HuggingFace's cache — redirect to the repo-local
    `/models` dir via HF_HOME env var to stay within CLAUDE.md Rule 6.
    """
    os.environ.setdefault("HF_HOME", str(REPO_ROOT / "models" / "hf"))
    from FlagEmbedding import BGEM3FlagModel  # type: ignore
    return BGEM3FlagModel("BAAI/bge-m3", use_fp16=True)


def embed(embedder, texts: list[str]) -> list[list[float]]:
    """Return dense vectors. bge-m3 emits dense + sparse + multi-vector
    from a single forward pass; Phase 3 uses dense only (ADR-0019).
    """
    out = embedder.encode(texts, batch_size=8, max_length=512)
    return out["dense_vecs"].tolist()


# --- Vector store ----------------------------------------------------

def upsert(target: str, chunks: list[Chunk], vectors: list[list[float]]) -> None:
    """Write vectors into Qdrant. `target` selects local (in-memory)
    vs cloud (QDRANT_URL + QDRANT_API_KEY)."""
    from qdrant_client import QdrantClient  # type: ignore
    from qdrant_client.models import Distance, PointStruct, VectorParams  # type: ignore

    if target == "local":
        client = QdrantClient(path=str(REPO_ROOT / ".rag-index"))
    elif target == "cloud":
        url = os.environ["QDRANT_URL"]
        api_key = os.environ["QDRANT_API_KEY"]
        client = QdrantClient(url=url, api_key=api_key)
    else:
        raise SystemExit(f"unknown target: {target} (expected 'local' or 'cloud')")

    client.recreate_collection(
        collection_name=COLLECTION_NAME,
        vectors_config=VectorParams(size=1024, distance=Distance.COSINE),
    )
    points = [
        PointStruct(
            id=i,
            vector=vec,
            payload={"chunk_id": ch.chunk_id, "text": ch.text, "offset": ch.offset},
        )
        for i, (ch, vec) in enumerate(zip(chunks, vectors))
    ]
    client.upsert(collection_name=COLLECTION_NAME, points=points)


# --- Main -------------------------------------------------------------

def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description="Seed the RAG corpus into a vector index.")
    parser.add_argument("--target", choices=["local", "cloud"], default="local")
    parser.add_argument("--dry-run", action="store_true", help="chunk only; skip embed + upsert")
    args = parser.parse_args(argv)

    if not CORPUS_PATH.exists():
        sys.stderr.write(
            f"corpus not found: {CORPUS_PATH}\n"
            f"see ADR-0019 implementation checklist — the zh-TW Wikipedia "
            f"Taiwan page lands in a follow-up commit.\n"
        )
        return 1

    text = CORPUS_PATH.read_text(encoding="utf-8")
    chunks = chunk_text(text)
    print(f"chunked {len(text):,} chars into {len(chunks)} chunks", file=sys.stderr)

    if args.dry_run:
        return 0

    embedder = load_embedder()
    vectors = embed(embedder, [c.text for c in chunks])
    print(f"embedded {len(vectors)} vectors (dim={len(vectors[0])})", file=sys.stderr)

    upsert(args.target, chunks, vectors)
    print(f"upserted to {args.target} collection={COLLECTION_NAME}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
