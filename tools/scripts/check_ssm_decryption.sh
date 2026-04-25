#!/usr/bin/env bash
# Pre-commit guard: every `aws ssm get-parameter` invocation in a CI
# workflow must also pass `--with-decryption`. Reason recorded in
# `docs/incidents.md` Incident 16 (2026-04-25): SecureString parameters
# return KMS ciphertext silently when `--with-decryption` is omitted,
# surfacing as misleading downstream errors (AccessDenied against a
# ciphertext-shaped resource ARN, in the original case) rather than
# loud at the SSM read step. Three hours of misdiagnosis is enough
# tuition; the static check is cheap insurance.
#
# Usage: pre-commit invokes this on changed `.github/workflows/*.ya?ml`
# files, passing them as positional args. Exits non-zero if any
# `aws ssm get-parameter` call lacks `--with-decryption` within its
# local 6-line context window (covers the backslash-continuation
# pattern we author).

set -euo pipefail

exit_code=0

for file in "$@"; do
  awk -v fname="$file" '
    /aws ssm get-parameter/ {
      start_line = NR
      block = $0
      # Peek 6 lines forward for backslash-continued args.
      for (i = 1; i <= 6; i++) {
        if ((getline next_line) > 0) {
          block = block "\n" next_line
        }
      }
      if (block !~ /--with-decryption/) {
        printf "%s:%d: aws ssm get-parameter without --with-decryption (Incident 16)\n", fname, start_line
        bad = 1
      }
    }
    END { exit bad ? 1 : 0 }
  ' "$file" || exit_code=1
done

exit "$exit_code"
