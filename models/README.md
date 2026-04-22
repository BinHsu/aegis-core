# /models/ — AI Model Artifacts

This directory holds the inference model files used by the Aegis Core
C++ engine. **Model files are NOT checked into git** — they are
downloaded on first build via `tools/scripts/download_models.sh` and
verified against the `manifest.json` in this directory.

## manifest.json

The `manifest.json` is the **single source of truth** for model
provenance, integrity, and licensing. Every model file must have a
corresponding entry in the manifest before it can be loaded by the
engine.

### Schema

See `manifest.schema.json` for the formal JSON Schema definition.
Key fields per model entry:

| Field | Purpose |
|---|---|
| `id` | Unique identifier used in code (`ResourceBudget`, config) |
| `filename` | File path relative to `/models/` |
| `sha256` | SHA-256 hex digest — **verified at load time** |
| `size_bytes` | Expected file size — verified after download |
| `estimated_ram_bytes` | Memory footprint — used by `ResourceBudget` (ADR-0010) |
| `origin_url` | Download URL |
| `origin_version` | Upstream release tag or commit hash |
| `license` | SPDX identifier — GPL/AGPL blocked (ARCH §10.1) |
| `required` | `true` for MVP models, `false` for optional |

### Integrity Verification

The C++ engine model loader performs the following checks at startup:

1. Read `manifest.json`.
2. For each entry where `required == true`:
   a. Check that the file exists at `filename`.
   b. Compute SHA-256 of the file.
   c. Compare against the `sha256` field in the manifest.
   d. **Refuse to mmap and abort startup** if the hash does not match.
3. For optional models (`required == false`): skip if file is absent;
   verify hash if file is present.

This prevents loading tampered or corrupted model files. See
threat-model.md threat T2.

### PGP Signing (Phase 4+)

In Phase 4+, `manifest.json` itself is signed with a PGP detached
signature (`manifest.json.sig`). The engine verifies the signature
before trusting the manifest contents. This closes the attack vector
where an attacker modifies `manifest.json` to match a malicious
model's hash (threat-model.md threat T6).

## Download Script

```bash
# Download every pinned model — whisper-tiny + bge-m3 (verifies
# SHA-256 after each download). Default since 2026-04-22.
./tools/scripts/download_models.sh

# Lightweight mode — only models that are required=true at engine
# startup. Skips bge-m3; suitable for CI / minimal installs.
./tools/scripts/download_models.sh --required-only

# Download a specific model by ID
./tools/scripts/download_models.sh --model whisper-large-v3-turbo-q4

# Verify existing models without downloading
./tools/scripts/download_models.sh --verify-only
```

The script reads `manifest.json`, downloads each required model from
`origin_url`, verifies the SHA-256 hash, and places the file in this
directory. It is idempotent — if a file already exists and its hash
matches, it is skipped.

## Adding a New Model

1. Choose the model and quantization. Verify the license is
   permissive (MIT, Apache-2.0, BSD). GPL/AGPL are blocked.
2. Download the model file and compute its SHA-256:
   ```bash
   curl -L <url> -o models/<filename>
   shasum -a 256 models/<filename>
   ```
3. Add an entry to `manifest.json` with all required fields.
4. Validate the manifest against the schema:
   ```bash
   # Requires a JSON Schema validator (e.g., ajv-cli)
   npx ajv validate -s models/manifest.schema.json -d models/manifest.json
   ```
5. Update `ARCHITECTURE.md` §6 if the new model changes the fixed
   overhead or per-session budget.
6. Run the WER golden audio regression suite (ADR-0011) if the model
   is a transcription or diarization model.
7. Commit under `build(deps): add <model-id> to model manifest`.

## Updating a Model Version

See ADR-0009 "Upstream whisper.cpp version bump procedure" for the
canonical process. In brief:

1. Download the new version.
2. Compute SHA-256.
3. Update the `sha256`, `size_bytes`, `origin_url`, and
   `origin_version` fields in `manifest.json`.
4. Run WER regression suite — any drift over threshold blocks the
   bump.
5. Commit under `build(deps): bump <model-id> to <version>`.

## Directory Hygiene

- Model files (`*.gguf`, `*.bin`, `*.onnx`, `*.safetensors`, etc.)
  are excluded from git via the root `.gitignore`.
- `manifest.json`, `manifest.schema.json`, and this `README.md` ARE
  tracked in git.
- Do not store model files in Git LFS — the download script is the
  canonical distribution mechanism (CLAUDE.md Rule 6: everything
  stays inside the repo tree once downloaded).

## Related

- `ARCHITECTURE.md` §6 AI Models & Hardware Resource Optimization
- `ARCHITECTURE.md` §10.1 Supply Chain Integrity
- `docs/adr/0009-cpp-build-and-toolchain.md` — whisper.cpp version
  bump procedure
- `docs/adr/0010-cpp-engine-runtime-architecture.md` —
  `ResourceBudget` and `estimated_ram_bytes`
- `docs/adr/0012-remove-voiceprint-matching.md` — no biometric
  models
- `docs/threat-model.md` threats T2, T6
