# ADR-0011: WER Golden Audio Test Fixtures

- **Status**: Accepted
- **Date**: 2026-04-11
- **Deciders**: Project owner, architect
- **Supersedes**: —
- **Superseded by**: —

## Context

`ARCHITECTURE.md` §10.5 mandates a Word Error Rate (WER) regression
suite as the **primary guard against model, quantization, and version
drift** in the transcription pipeline. ADR-0005 bans debug audio
dumps from production builds (R7) and forbids persistent storage of
real user audio anywhere in the system — which means we cannot
regression-test against real customer data. Test integrity (CLAUDE.md
Rule 2) forbids stub tests.

We need a curated set of **golden audio fixtures** that:

1. Exercise Aegis's realistic target scenarios.
2. Are legally clean — no ambiguous license terms, no privacy
   exposure.
3. Fit within CI time budget (see D4 below).
4. Detect real quality regressions without false positives from
   natural model variance.
5. Can be extended by a solo contributor without re-engineering the
   pipeline.

This ADR picks a source strategy, a computation tool, threshold
structure, storage location, and the CI integration pattern.

## Decision Drivers

- **D1. Legal cleanliness** — no user audio, no copyright ambiguity,
  permissive license or public domain.
- **D2. Target scenario coverage** — English meeting speech, Traditional
  Chinese meeting speech, Mandarin / English code-switching,
  multi-speaker conversation, common meeting acoustic noise (keyboard
  typing, distant chatter, air conditioning hum).
- **D3. Stability** — the suite must not yield different scores on
  different CI runs without code changes. Determinism is mandatory.
- **D4. CI speed** — WER regression computation must complete within
  a few minutes per PR. Ten-minute CI cycles are acceptable; hour-long
  cycles are not.
- **D5. Detectability** — the suite must surface a real regression (a
  model downgrade, a quantization change that loses accuracy, a
  sample-rate bug) without tripping on natural model variance (e.g.,
  a whisper.cpp upstream minor version that shuffles floats).
- **D6. Maintainability** — adding a fixture later must require only
  editing one directory and one JSON file, not a code change.
- **D7. Repository size discipline** — fixtures stored in-repo must
  not bloat clone time significantly. Target: total fixture footprint
  ≤ 20 MB.

## Sub-decisions

### Sub-decision 1 — Source of Audio Fixtures

#### Options

- **(a) LibriSpeech `test-clean` only** — widely used academic
  benchmark for ASR; CC-BY 4.0. Thousands of English utterances.
  **Fatal**: no Traditional Chinese coverage.
- **(b) Mozilla Common Voice only** — CC0 public domain, includes
  Mandarin. Thin on Traditional Chinese specifically; Traditional /
  Simplified labeling is inconsistent across contributions.
- **(c) Self-recorded only** — project-owner or contributor records
  all fixtures. Total control, but high upfront labor and no external
  baseline for comparison.
- **(d) Hybrid: LibriSpeech + self-recorded** ✅ — small curated
  subset of LibriSpeech for English baseline + self-recorded
  Traditional Chinese and code-switch fixtures.

#### Chosen: **(d) Hybrid — LibriSpeech test-clean + self-recorded**

**Composition for MVP** (≈13 fixtures, ≈5–10 minutes total audio):

| Category | Count | Source | Language | Notes |
|---|---|---|---|---|
| English clean speech | 3 | LibriSpeech test-clean | en | Single-speaker, studio-quality baseline |
| English meeting speech | 2 | Self-recorded | en | Laptop mic, conversational tone |
| Traditional Chinese clean | 3 | Self-recorded | zh-TW | Studio-quality, single-speaker |
| Traditional Chinese meeting | 2 | Self-recorded | zh-TW | Laptop mic, 2 speakers |
| Mandarin / English code-switch | 2 | Self-recorded | mix | Real-world bilingual meeting style |
| Noise-robust | 1 | Self-recorded | mix | Keyboard, air-conditioning background |

**Why**:

- **D1**: LibriSpeech test-clean is CC-BY 4.0; attribution in
  `test/golden_audio/LICENSE.md` is sufficient. Self-recorded
  fixtures are the project owner's own voice under a clear
  self-license.
- **D2**: covers all five target scenarios. Mandarin / Traditional
  Chinese specifically is not well-served by off-the-shelf
  datasets, so self-recording is the only path.
- **D4**: 13 fixtures × ~30 seconds = ~7 minutes of audio → WER
  computation completes in ~2 minutes on CPU whisper inference
  (Phase 4 CI).
- **D5**: the academic LibriSpeech baseline provides a stable
  "ground truth" against the published whisper-large-v3 benchmark
  — if our score diverges materially from the public benchmark,
  we have a regression. Self-recorded fixtures provide
  domain-specific coverage that benchmarks cannot.
- **D6**: adding a fixture = drop a `.wav` + `.txt` + update
  `test/golden_audio/manifest.json`.
- **D7**: 16 kHz mono 16-bit at ~7 minutes total ≈ **13 MB** —
  well under the 20 MB budget.

#### Why Not Each Alternative

- **(a)**: fails D2 — no Traditional Chinese coverage is a
  non-starter for a product whose primary use case is executive
  meetings in Mandarin-speaking enterprises.
- **(b)**: Traditional Chinese quality in Common Voice is too
  inconsistent to serve as a regression baseline. Revisit as a
  **secondary** source in Phase 3+.
- **(c)**: no external baseline makes it harder to detect whether
  a drift is "our fixtures aged" vs "the model regressed." The
  LibriSpeech anchor is cheap insurance.

#### Ethics / Privacy Note

Self-recorded fixtures must be recorded by contributors who
explicitly release the audio under a permissive license into
`test/golden_audio/LICENSE.md`. No user, customer, or third-party
audio ever enters the fixture set. This is a hard rule; violations
are a privacy incident.

---

### Sub-decision 2 — WER / CER Computation Tool

#### Options

- **`jiwer`** ✅ — actively maintained BSD-3 Python library.
  Computes WER, CER, MER, WIL, and WIP. The de facto standard in
  the ASR evaluation community.
- **Hand-rolled WER** — implement Levenshtein-based WER ourselves.
  Unnecessary — WER is subtler than it looks (handling of
  punctuation, capitalization, numeric normalization), and `jiwer`
  already solves these correctly.
- **`sclite` (SCTK)** — SRI's historical sclite tool. Powerful but
  hard to package with Bazel and harder to reason about in CI logs.

#### Chosen: **`jiwer`**

**Why**:

- BSD-3 license is permissive and compatible with Aegis's
  packaging.
- Handles both WER (for English) and CER (for Traditional Chinese
  — character error rate, which is the normal metric for CJK
  languages where "word" boundaries are ill-defined).
- Built-in text normalization transforms (lowercase, strip
  punctuation, unicode normalization) that we can apply
  consistently across fixtures.
- Python wheel, installs cleanly via `pip` in CI and (if needed)
  via a `rules_python` `pip_parse` in Bazel.
- Widely understood — reviewers do not have to re-learn a niche
  tool.

#### Normalization Pipeline

Before comparing reference to hypothesis, both strings pass through
a deterministic normalization pipeline:

```python
from jiwer import Compose, ToLowerCase, RemovePunctuation, Strip
from jiwer import RemoveMultipleSpaces, ExpandCommonEnglishContractions

en_transform = Compose([
    ToLowerCase(),
    ExpandCommonEnglishContractions(),
    RemovePunctuation(),
    RemoveMultipleSpaces(),
    Strip(),
])

zh_transform = Compose([
    # Do NOT lowercase (no case in CJK)
    # Do NOT expand contractions (N/A)
    RemovePunctuation(),
    RemoveMultipleSpaces(),
    Strip(),
])
```

This is deterministic (**D3**) and documented alongside the fixtures
in `test/golden_audio/README.md`.

---

### Sub-decision 3 — Thresholds and Pass/Fail Semantics

#### Target Thresholds

Per the ARCH §10.4 SLO table:

| Fixture category | Metric | Threshold | Rationale |
|---|---|---|---|
| English clean (LibriSpeech) | WER | ≤ 5% | whisper-large-v3 is benchmarked at 2.7–3.8% on LibriSpeech test-clean; we allow headroom for our turbo-Q4 quantization |
| English meeting speech | WER | ≤ 8% | Real-world laptop mic audio is harder than studio |
| Traditional Chinese clean | CER | ≤ 8% | CJK CER baselines typically loosen relative to English WER |
| Traditional Chinese meeting | CER | ≤ 12% | Meeting noise + 2 speakers |
| Code-switch mix | CER | ≤ 15% | Notoriously hard; whisper handles it OK but not great |
| Noise-robust mix | CER | ≤ 15% | Similar to code-switch class |

**Per-fixture thresholds**, not aggregate. If any single fixture
regresses past its threshold, CI blocks merge. Aggregate statistics
are informational.

#### Why Per-Fixture

- A regression on one fixture is often a narrow bug (e.g., an
  obscure phoneme, a specific noise type). Aggregate averages can
  hide this.
- Per-fixture thresholds make failures **actionable**: "this
  specific audio exceeded its bound, here is the diff."
- Debugging is easier because there is one failing case, not a
  suite-wide drift.

#### Baseline Refresh Process

Thresholds are set once during Phase 2 based on actual initial
measurements with whisper-large-v3-turbo Q4. They are **not**
re-baselined casually:

- A whisper.cpp version bump that moves scores is the expected
  trigger for a baseline review.
- A quantization change (e.g., Q4 → Q5) is the expected trigger.
- A regression of 0.5% within a single fixture's threshold is
  acceptable variance; exceeding the threshold is not.
- Baseline updates land in a dedicated PR (`test: refresh WER
  baseline for whisper.cpp vX.Y.Z`) with before/after numbers in
  the commit message.

#### Pass/Fail Reporting

CI output format (per-fixture):

```
[PASS] en_clean_001        WER   2.3%  (threshold  5.0%)
[PASS] en_meeting_001      WER   4.1%  (threshold  8.0%)
[PASS] zh_clean_001        CER   5.2%  (threshold  8.0%)
[FAIL] zh_meeting_002      CER  13.4%  (threshold 12.0%)
[PASS] codeswitch_001      CER  11.7%  (threshold 15.0%)
```

Failed fixtures include a diff of reference vs hypothesis in the
CI log to speed up debugging.

---

### Sub-decision 4 — Fixture Storage Location and Format

#### Directory Layout (per ADR-0008)

```
test/golden_audio/
├── LICENSE.md                        # per-fixture license attribution
├── README.md                         # how to add a fixture
├── manifest.json                     # fixture metadata + thresholds
├── en/
│   ├── en_clean_001.wav
│   ├── en_clean_001.txt
│   ├── en_clean_002.wav
│   ├── en_clean_002.txt
│   └── ...
├── zh/
│   ├── zh_clean_001.wav
│   ├── zh_clean_001.txt
│   └── ...
├── codeswitch/
│   ├── codeswitch_001.wav
│   ├── codeswitch_001.txt
│   └── ...
└── noise/
    ├── noise_001.wav
    └── noise_001.txt
```

#### File Format Requirements

- **Audio**: WAV, **16 kHz mono 16-bit PCM** (matches whisper.cpp
  native input; no runtime resampling in CI).
- **Reference transcripts**: UTF-8 encoded `.txt` files, one line
  per utterance, matching the audio filename.
- **Metadata**: `manifest.json` captures per-fixture threshold,
  language, speaker count, duration, source, and license:

```json
{
  "schema_version": 1,
  "fixtures": [
    {
      "id": "en_clean_001",
      "audio": "en/en_clean_001.wav",
      "reference": "en/en_clean_001.txt",
      "language": "en",
      "speaker_count": 1,
      "duration_seconds": 12.4,
      "sample_rate_hz": 16000,
      "source": "LibriSpeech test-clean (subset)",
      "source_license": "CC-BY 4.0",
      "source_attribution": "see LICENSE.md",
      "metric": "WER",
      "threshold": 0.05,
      "category": "en_clean"
    }
  ]
}
```

#### Storage Strategy

- Files live **directly in the git repository**, not Git LFS, for
  Phase 1. Rationale: "clone and run minimal struggle" (CLAUDE.md
  Rule 6 / ARCHITECTURE.md ethos) is violated by Git LFS, which
  requires a separate installation step and an external content
  server.
- Total footprint target: **≤ 20 MB** (conservative; realistic is
  ~13 MB at the Phase 1 fixture count).
- **Git LFS re-evaluation trigger**: if fixture count grows past
  50 (~80 MB) or total repo clone time exceeds 60 seconds on a
  typical connection, consider migration. This is a Phase 5+
  decision.

#### Why Not External Storage (S3 / ...)

- Violates the "clone and run" ethos.
- Introduces a network dependency in CI for every PR.
- Adds credential handling complexity to CI.

---

### Sub-decision 5 — CI Integration

#### Phase 1 (Before Bazel Targets Exist)

No WER CI job. The `.github/workflows/ci-baseline.yml` skeleton in
Phase 0 runs lint / secret scan / buf lint only. WER enforcement
is wired up in **Phase 2** when the C++ engine gRPC server is
buildable.

#### Phase 2+ CI Job

A new job in `.github/workflows/wer-regression.yml`:

```yaml
name: WER Regression

on:
  pull_request:
    branches: [main]
    paths:
      - 'engine_cpp/**'
      - 'models/manifest.json'
      - 'test/golden_audio/**'
      - 'proto/**'
      - '.github/workflows/wer-regression.yml'
  schedule:
    - cron: '0 5 * * *'   # nightly baseline-drift detection

jobs:
  wer:
    runs-on: ubuntu-latest
    timeout-minutes: 20
    steps:
      - uses: actions/checkout@v4
      - name: Cache Bazel
        uses: actions/cache@v4
        with:
          path: .bazel_cache
          key: bazel-wer-${{ hashFiles('MODULE.bazel') }}
      - name: Download and verify models
        run: ./tools/scripts/download_models.sh
      - name: Build engine
        run: ./tools/bazelisk build //engine_cpp:engine --config=cpu
      - name: Run WER regression
        run: ./tools/bazelisk test //test:wer_regression --config=cpu
      - name: Upload diff on failure
        if: failure()
        uses: actions/upload-artifact@v4
        with:
          name: wer-diff
          path: bazel-testlogs/test/wer_regression/test.log
```

Key points:

- Uses `--config=cpu` (no GPU required on CI runners — the cheap
  baseline).
- Uses `path` filter to avoid re-running on pure-doc PRs.
- Nightly cron catches drifts unrelated to PR changes (e.g., an
  upstream model manifest bump via Dependabot).
- Timeout at 20 minutes provides headroom. The ~2 minute estimate
  is based on whisper CPU inference at ~3.5x real-time; actual
  GitHub Actions runner performance may be slower — tune after
  Phase 2 brings the first real CI run.

---

## Decision Outcome — Summary

| Concern | Choice |
|---|---|
| Source of audio | LibriSpeech test-clean subset + self-recorded Traditional Chinese and code-switch |
| Fixture count (Phase 1) | ~13 fixtures, ≈7 minutes total audio, ≈13 MB |
| WER tool | `jiwer` (Python, BSD-3) |
| Metric | WER for English, CER for Traditional Chinese and code-switch |
| Thresholds | **Per-fixture**, not aggregate; en-clean ≤ 5% WER, zh-clean ≤ 8% CER, meeting/codeswitch/noise loosened |
| Storage | In-repo (not Git LFS); re-evaluate at 50+ fixtures or 80+ MB |
| CI integration | Phase 2+; path-filtered PR run + nightly cron |

## Consequences

### Positive

- Covers all five target scenario classes on day one.
- Legally clean (permissive licenses on all audio).
- CI-compatible (fits in ~2 minute budget on CPU build).
- Small repo footprint; no LFS dependency.
- Per-fixture thresholds make failures actionable.
- Phase 1 writes the baseline; Phase 2 wires the CI enforcement;
  Phase 3+ can extend the fixture set without code change.
- Deterministic normalization pipeline ensures CI reproducibility.

### Negative

- **Self-recording takes real human effort**. Project owner must
  record ~7 fixtures at studio or near-studio quality. Not free.
- **Initial thresholds are guesses**. They will be tuned once we
  run against the real whisper-large-v3-turbo Q4 model in Phase 2.
- **Whisper version bumps may cause synchronized regressions**
  across multiple fixtures, requiring judgment calls on whether
  the regression is real or a natural variance in a new version.
- **No speech from native Mandarin speakers other than the
  project owner** in the initial MVP. This is a diversity gap;
  more fixtures from other contributors land in Phase 3+.

### Risks

- **Fixture theft** — someone copies Aegis's fixture set for a
  competing product. Mitigated by permissive licensing (we do not
  mind — that is the point of CC-BY and self-released content).
- **WER drift from upstream Python jiwer version bumps** — the
  normalization pipeline may change subtly between jiwer releases.
  Mitigation: pin jiwer version in `tools/ci/requirements.txt`
  and bump deliberately, re-baselining if behavior changes.
- **Fixture repository poisoning via malicious PR** — a bad actor
  submits a fixture whose reference transcript is wrong, causing
  false failures. Mitigation: CODEOWNERS requires maintainer
  review on any `test/golden_audio/` change.

## Open Implementation Questions (Phase 2)

Not blocking this ADR; noted for the Phase 2 engineer:

- **Speaker count in diarization validation**: should WER tests
  also validate that `speaker_count` in diarization output matches
  the fixture metadata? Proposal: yes, as a secondary assertion,
  not a hard fail.
- **Per-fixture WER historical tracking**: should CI archive each
  run's WER scores for long-term drift visualization? Proposal:
  publish to a simple JSON artifact, visualize with a lightweight
  tool in Phase 4.
- **Self-recording tooling**: should there be a `tools/scripts/record_fixture.sh`
  helper that captures audio, resamples to 16 kHz mono, and
  creates the `manifest.json` entry? Proposal: yes, adds once the
  fixture count grows past 5.

## Related

- ADR-0005 Audio Ephemeral Policy (R7 no debug dump → cannot use
  real user audio for testing)
- ADR-0008 Monorepo Folder Structure (`test/golden_audio/`
  location)
- ADR-0009 C++ Build and Toolchain (CI runs `--config=cpu`)
- ADR-0010 C++ Engine Runtime Architecture (the engine under
  test)
- `ARCHITECTURE.md` §10.4 SLOs (WER threshold source of truth)
- `ARCHITECTURE.md` §10.5 Test Integrity (WER suite as the
  primary drift guard)
- `CLAUDE.md` Rule 2 Testing Integrity
- [jiwer documentation](https://github.com/jitsi/jiwer)
- [LibriSpeech dataset](https://www.openslr.org/12)
