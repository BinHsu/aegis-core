# tools/rag — RAG corpus → vector index seed pipeline

Implements [ADR-0019](../../docs/adr/0019-rag-corpus-and-embedding-pipeline.md).
Reads a markdown corpus, chunks it Chinese-aware, embeds the chunks
with `BAAI/bge-m3`, and writes the result into Qdrant (local
file-backed for the demo, Qdrant Cloud for deploy).

## What ships in git vs what gets rebuilt

| Path                              | In git | Why |
|-----------------------------------|--------|-----|
| `docs/rag/taiwan.md`              | yes    | The corpus IS the truth. Human-readable, diffable, attributable. |
| `tools/rag/seed.py`               | yes    | Reproducibility recipe. |
| `tools/rag/requirements.txt`      | yes    | Pinned versions = the same `seed.py` produces the same index. |
| `.rag-index/`                     | **no** | Derivative artifact; gitignored. Bumping the embedding model regenerates it; committing would diff-noise every rebuild. |

This is the **immutable corpus, reproducible index** rule from
ADR-0019. The corpus is authored once and edited deliberately; the
index is a function of (corpus, `seed.py`, pinned model version) and
is rebuilt anywhere it's needed.

## Setup (once)

Per [CLAUDE.md Rule 6](../../CLAUDE.md), the Python virtualenv lives
inside the repo so a clone never pollutes the host system:

```bash
python3 -m venv .venv
source .venv/bin/activate
pip install -r tools/rag/requirements.txt
```

The first `seed.py` invocation downloads `BAAI/bge-m3` weights
(~2.2 GB) into `models/hf/` — `seed.py` sets `HF_HOME` accordingly so
the cache stays inside the repo. Subsequent runs are cache-hits.

## Seed the bundled corpus

```bash
python tools/rag/seed.py --target=local
```

Writes the index under `.rag-index/` (Qdrant on-disk format,
collection `aegis_taiwan_zh_tw`).

## Seed your own corpus

The bundled `docs/rag/taiwan.md` is one example. Any UTF-8 markdown
file with paragraph-level structure substitutes in:

```bash
python tools/rag/seed.py --corpus path/to/your-corpus.md --target=local
```

Section headers (`##`) help human review and give the chunker a
stronger natural break, but are not required — the
Chinese-aware splitter falls back to `\n\n`, then `\n`, then
`。！？，` so unformatted text still chunks reasonably.

## Push to a cloud vector store

Same code path, same chunking, same embeddings — only the write
target differs:

```bash
export QDRANT_URL=https://your-cluster.qdrant.io
export QDRANT_API_KEY=...
python tools/rag/seed.py --target=cloud
```

Per ADR-0019, the cloud index is **rebuilt at deploy time from the
same `docs/rag/*.md`** — never shipped as a binary blob and never
diff'd between local-index and cloud-index. If those two ever
disagree, the rule is "rebuild from corpus" not "diff and reconcile."

## Common smoke test

```bash
python tools/rag/seed.py --dry-run
# expected: "chunked 1,353 chars into N chunks from docs/rag/taiwan.md"
```

If `--dry-run` succeeds but a real seed fails, the failure is in
embedding or Qdrant, not in chunking — narrows the search.

## Troubleshooting

- **`corpus not found`**: pass `--corpus PATH`, or land your file at
  `docs/rag/taiwan.md`.
- **`Killed: 9` on macOS during embedding**: bge-m3 wants real RAM
  during the model load. Close other heavy apps; if you're on an
  8 GB machine the FP16 mode (`use_fp16=True`, set in `seed.py`) is
  the smallest viable footprint.
- **Slow first run, fast second run**: weights download on first run.
  This is expected — the `models/hf/` cache fixes it.
- **Index corrupt / want a fresh start**: `rm -rf .rag-index/` and
  re-run. The corpus is untouched (it lives at `docs/rag/`).
