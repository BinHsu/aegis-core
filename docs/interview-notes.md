# Interview Notes — Aegis Core

> **Who this document is for.** Recruiters, hiring managers, and HR
> partners evaluating me for a role. It is deliberately written without
> jargon; if you can read a business email, you can read this. A
> separate technical walkthrough lives in `README.md` and
> `docs/adr/` — hand those to your engineering team when you want
> depth, and they can verify every claim below in the code itself.
>
> **Reading time.** 7 minutes.

---

## The 30-second version

I am a **generic architect** — I make the cross-cutting decisions on a
software project (which languages, which protocols, how the pieces fit
together, what's in scope, what isn't) rather than specializing in one
layer of the stack. This repo (`aegis-core`) is evidence of that, and
a companion repo (`landing-zone`) shows the same pattern applied to
cloud infrastructure.

My style: **pick proven open-source building blocks, compose them
honestly, and write down every decision so the next person on the team
can audit, challenge, or replace it**.

If your team needs someone who owns "the shape of the whole system"
rather than a single component — that's the role I do best.

---

## What this project is, in plain language

**Aegis Core** is a live-transcription tool for meetings. Think: a
meeting starts, the host's browser picks up the audio, a speech-to-text
model turns it into text on the fly, and invited viewers see the
transcript streaming in real time on their own devices.

The novel constraint is **privacy**: in "Local mode" (a company's own
laptop / server), the audio never leaves the host's machine — the
transcription happens locally, and only the text is sent to viewers. In
"Cloud mode" (for teams who prefer centralized operation), the same
software runs on a cloud server with the same guarantees about what
gets stored where.

**What's working today** (clone the repo and see for yourself):

1. One command brings up the whole system: `bazel run //:app_local`.
2. A real microphone recording goes in; a real transcript comes out.
3. The automated test suite proves it end-to-end against a reference
   audio clip.

---

## What this repo demonstrates about me as a hire

Each bullet below is backed by something concrete in the repo that your
technical team can verify.

- **I make decisions and write them down.** The `docs/adr/` folder
  contains 14 architecture decision records. Each one names the
  problem, the options considered, the choice made, and the reasoning.
  When I change my mind, I write a new ADR — I don't quietly rewrite
  history.

- **I ship end-to-end, not component-by-component.** The project
  spans three programming languages (C++ for the speech-recognition
  core, Go for the server that glues everything together, TypeScript
  for the web frontend). All three build under one tool chain, all
  three share one set of protocol definitions, and a single command
  starts the whole thing.

- **I am honest about what I deliberately did not build.** The
  `README.md` and `ROADMAP.md` both contain a "Known Gaps" section
  that names exactly what is missing, why it was skipped, and what a
  future contributor would need to do to close each gap. This
  discipline is usually more telling than a feature list — it is the
  signal that I know how to say "this is out of scope for now", which
  is rarer than "I built everything I could think of".

- **I choose boring, well-maintained dependencies.** I did not
  reinvent speech recognition, I integrated `whisper.cpp`. I did not
  write a WebRTC implementation, I used `pion/webrtc`. I did not
  design a new token format, I used industry-standard JWT. My
  contribution is **correctly assembling trusted pieces** and verifying
  the assembly in tests.

- **I build for the next engineer's benefit.** Every non-obvious
  design choice has an inline comment explaining *why* it was made,
  not just what it does. The goal is that someone new to the
  codebase can work productively within a day — not a week.

- **I catch the things a generalist should catch.** One concrete
  example: when the code validates authentication tokens, there's a
  specific test that confirms the classic `alg=none` downgrade attack
  is rejected (this is a JWT implementation pitfall that has
  historically compromised production systems). I know the class of
  bug to watch for and encode that knowledge in tests.

- **I follow rules I've written for myself.** The repo contains
  `CLAUDE.md`, a charter of engineering rules — about testing, about
  documentation, about incident postmortems — that I hold myself to. I
  can point at my own commits and show the charter being followed,
  including a postmortem log of real dev-time problems I hit and fixed
  along the way (`docs/incidents.md`).

---

## What I am *not* claiming

I am being specific here because a mismatched hire is expensive for
everyone. If the role is centered on any of the following, I am
probably not the strongest candidate:

- **Deep machine-learning research.** I integrate a pre-trained
  speech-recognition model. I do not train, fine-tune, or redesign
  models. If the role is "improve our speech-recognition accuracy by
  2 percentage points", that's a specialist's job.

- **Real-time audio signal processing.** I pick libraries that handle
  audio conversion correctly. I do not write echo cancellation
  algorithms or jitter buffers from scratch.

- **Cryptographic primitive design.** I use established libraries and
  know the common ways to *misuse* them (hence the `alg=none` test).
  I do not invent new encryption schemes.

- **Deep Kubernetes / cloud-operations work.** There *is* an ADR in
  this repo explaining how the system is meant to be deployed, but the
  actual cloud-infrastructure evidence lives in a separate repo
  (`landing-zone` — mention it to me and I can send the link and a
  similar walkthrough).

---

## What kind of role fits

**Target level.** Staff / Principal engineer; architect; tech lead.
Someone responsible for "the shape of the thing", not a single
sub-component.

**Target function.** Backend and platform architecture. Designing
protocols, choosing build systems, setting team conventions, mentoring,
writing ADRs, running technical reviews.

**Where the two repos land.**

- `aegis-core` (this repo) — **backend + platform** architecture. How
  services are designed, how the code is structured, how the build
  works, how contracts between languages are maintained.
- `landing-zone` (separate repo) — **cloud infrastructure**
  architecture. How the above runs on AWS under strict compliance,
  deployment pipelines, cost visibility, incident response.

Ask me about one or the other depending on what you are screening for.

---

## For your technical team

If your engineers want to verify any of the above, point them at these
entry points in order — the whole walkthrough takes about 15 minutes:

1. **`README.md`** → start here. "Quick Start", "Known Gaps", ADR
   index table.
2. **`docs/adr/`** → the 14 architecture decisions. Each is under 300
   lines and explicitly names its alternatives.
3. **`ROADMAP.md` → Phase 2 Known Gaps** → what was deliberately not
   built, and why. This is the "judgment" evidence.
4. **`docs/incidents.md`** → real dev-time postmortems, including
   failed fix attempts and red herrings. This is the "intellectual
   honesty" evidence.
5. Any specific decision flagged in the ADR index — click through to
   read the reasoning, then look at the corresponding code path
   referenced by the ADR.

Your team should feel free to challenge any decision. I won't be
offended — that's literally what ADRs exist for.

---

## Contact

Reach me via the GitHub profile linked from the repo's top-level
`README.md`. If anything above matches a role you're screening for, I
can prepare a 20-minute walk-through against a real running instance of
the system. Most candidates will hand you a deck; I'll hand you a
terminal.

---

*This document is maintained alongside the code. If anything here
stops matching reality, the code wins and this file gets a correction
pull request.*
