# ADR-0022: Cloud-mode multi-tenancy isolation in the RAG vector store

| Field    | Value |
| -------- | ----- |
| Status   | Proposed — deferred to Phase 4 Cognito JWT wiring |
| Date     | 2026-04-17 |
| Deciders | Project author |
| Context  | Phase 3b Slice 6 ships `engine seed` writing RAG corpus chunks to Qdrant (Qdrant Cloud for demo, self-hosted in Phase 4). The current payload schema is `{text, source_path, chunk_index}` — no tenant or user identity. This ADR records how to evolve that schema for cloud-mode multi-tenancy *without* implementing it yet, because the JWT source (Cognito) is not live yet. |
| Related  | ADR-0001 (Cognito SSO + session tokens), ADR-0019 (RAG corpus pipeline), ADR-0020 (engine owns inference), `docs/threat-model.md` §Open Items |

## Context

Phase 3b's RAG seed path (`engine seed --corpus PATH --target=cloud`)
is about to enable multi-user cloud deployments. Once two different
SSO users upload their own corpora, the Qdrant collection they share
will mix both users' chunks. Without identity-scoped filtering, every
authenticated user sees every other user's corpus — a straight
information-disclosure vulnerability per `threat-model.md` STRIDE
"Information Disclosure".

The demand is specific: **"when staff logs in to open a meeting, the
query path must retrieve only their own uploaded corpus, not another
user's"**. Three standard design choices exist for multi-tenant
vector stores:

### Option A — Per-user collection (hard isolation)

Each SSO user gets their own Qdrant collection:
`aegis_user_<sub>`.

- **Pros**: Hardest boundary possible. A query-time bug that forgets
  to filter cannot leak cross-user because you literally cannot
  search another user's collection without naming it explicitly.
- **Cons**: Collection count grows with user count. Qdrant Cloud
  free tier is 1 GB / 1 node and allows many collections, but
  creation rate-limits and per-collection fixed overhead mean
  "thousands of users, one collection each" becomes operational
  drag. Cross-user analytics (e.g., "how many total chunks across
  the org?") become N RPC calls.

### Option B — Shared collection + payload filter (soft isolation)

One large collection per corpus type. Every point carries
`user_id` in its payload. Query applies a Qdrant filter:
`must: [{key: "user_id", match: {value: <caller sub>}}]`.

- **Pros**: Cheapest collection-count footprint. Cross-user
  analytics are a single RPC.
- **Cons**: Isolation depends on every query correctly applying
  the filter. One code path forgetting it = data leak across all
  users in the collection. Audit discipline is load-bearing.

### Option C — Tenant collection + user payload filter (hybrid)

One collection per **tenant** (enterprise customer):
`aegis_<tenant_id>_<corpus_stem>`. Every point carries `user_id`
in payload. Query applies the user filter **within** the tenant
collection.

- **Pros**: Hard boundary for the blast-radius-critical axis
  (tenant — different paying customers). Soft boundary for the
  less-critical axis (users within the same tenant, who typically
  share org-level access anyway). Collection count is bounded by
  tenant count, which grows slower than user count.
- **Cons**: Middle complexity. Enterprise customers who want
  strict per-user separation inside their tenant must layer their
  own controls; the "soft boundary within tenant" default matches
  typical enterprise expectations but not all.

## Decision

**Adopt Option C (hybrid)** when Phase 4 wires in Cognito JWT
federation on the gateway.

### Schema shape

Collection naming:

```
aegis_<tenant_id>_<corpus_stem>
```

Where `tenant_id` comes from the Cognito JWT's `custom:tenant_id`
claim (per ADR-0001) and `corpus_stem` is the sanitized corpus
filename (Slice 6 already implements this for the single-tenant
demo via `DeriveCollectionName`).

Payload shape (extends Slice 6's `{text, source_path, chunk_index}`):

```
text:         <chunk text>           # existing
source_path:  <corpus file path>     # existing
chunk_index:  "<N>"                   # existing
user_id:      <cognito sub>          # new — UUID string
tenant_id:    <cognito custom:tenant_id>  # new — double-check, redundant with collection
created_at:   <ISO 8601 UTC>         # new — audit trail
```

Query path:

```
gateway:  extracts sub + tenant_id from Cognito JWT, forwards via
          gRPC metadata to engine
engine:   routes to collection `aegis_<tenant_id>_<corpus>`, applies
          filter `must: [{key: "user_id", match: {value: <sub>}}]`
```

### Why `tenant_id` is in BOTH collection name AND payload

Collection-name boundary is load-bearing for the hard isolation.
Payload's `tenant_id` is a **defense-in-depth redundancy**: if a
future refactor accidentally routes a query to the wrong
collection (e.g., a typo in the collection-name builder), the
payload filter will still reject points from other tenants. One
line of extra Qdrant filter cost; real defense value.

## Deferred decision + demo-horizon posture

**Why this ADR is Proposed, not Accepted**: implementing it now
would add `user_id` and `tenant_id` fields to the Slice 6 seed
payload with no honest source to populate them. There is no live
Cognito User Pool yet (Phase 2's `StaticJWTProvider` is a
shared-secret scaffold, not federated identity). Stubbing the
fields with `"demo"` / `"demo"` would create a schema that cannot
be validated at seed time AND gives a false sense of
multi-tenancy capability to reviewers reading the code.

The demo horizon's posture is:

- Phase 3b Slice 6 ships `engine seed` with the narrow
  `{text, source_path, chunk_index}` payload.
- `docs/threat-model.md` §Open Items explicitly calls out
  multi-tenant RAG isolation as unimplemented — any multi-user demo
  deploy today leaks across users within the same Qdrant.
- Phase 4 implementation bundle: Cognito JWT on the gateway
  (closes ADR-0001's descoped item), engine-side claim extraction
  through gRPC metadata, payload schema bump to add `user_id` /
  `tenant_id` / `created_at`, QdrantClient filter parameter on
  `Search`, collection-name change to include `tenant_id` prefix.

### Phase 4 implementation note — re-evaluate claim set before finalizing

When the Phase 4 Cognito wiring actually lands, **do not just
grab `sub` + `custom:tenant_id` and call it done**. Enumerate the
full Cognito claim set surfaced by the User Pool at that moment
and re-ask: does any available claim enable tighter isolation
than this ADR sketched? Candidates Cognito is known to emit
(exact set depends on User Pool configuration at provisioning
time):

- `sub` — user UUID (this ADR's minimum).
- `cognito:groups` — group memberships. Could map to a
  role-based collection scope (e.g. only surface corpora uploaded
  by members of the caller's `admin` or `hr` group).
- `cognito:username` — logical username. Usually redundant with
  `sub` but occasionally useful for audit trails.
- `custom:tenant_id` — tenant scope (this ADR's assumption —
  verify it's actually provisioned when Cognito lands).
- `custom:<anything>` — SAML / OIDC federation can pass through
  arbitrary claims (department, manager, cost-center, security
  clearance level) that sharper organizations use for fine-grained
  isolation.

If Phase 4's User Pool provides claims that cleanly sharpen the
boundary beyond `user_id` + `tenant_id`, amend this ADR (supersede
with ADR-00XX) rather than silently widening the payload schema.
**User directive 2026-04-17: "我記得之後是會整 cognito 就看這個
認證可以帶什麼資訊更具有獨立性."**

### Migration when Phase 4 lands

Existing Slice 6 Qdrant data from the demo (single-tenant,
single-user) is not automatically compatible with the Phase 4
schema. Migration options:

1. **Drop and re-seed**: demo corpora are small (<1 MB each) and
   cheap to re-embed. Acceptable at demo scale.
2. **In-place patch**: run a one-shot script that scans existing
   collections, adds default `user_id="demo"` + `tenant_id="demo"`
   to every point's payload, renames collection from
   `aegis_<corpus>` to `aegis_demo_<corpus>`. Preserves upload
   history but adds operational complexity.

Default: option 1 (drop and re-seed). Document in a Phase 4
migration runbook at implementation time.

## Consequences

### Positive

- **Tenant isolation is structural, not procedural.** A buggy query
  filter cannot leak cross-tenant because the collection boundary
  rules it out before the filter runs.
- **Forward compatibility** with Qdrant's built-in filter
  capabilities — no custom indexing or schema hacks needed.
- **Enterprise sales story**: "your data lives in its own Qdrant
  collection, not co-mingled with other customers" is a clean
  one-liner for security reviews.

### Negative

- **Schema migration cost when Phase 4 lands**: existing demo data
  must either be dropped and re-seeded, or patched in place.
  Budgeted as a one-shot Phase 4 migration task, not ongoing work.
- **Collection count scales with tenant count**: a 1000-tenant
  deployment = 1000+ collections. Qdrant handles this fine in
  principle but operational tooling (observability, backup,
  lifecycle) must keep up.
- **"User within tenant" isolation is soft.** A query that forgets
  the `user_id` filter leaks within-tenant. Code review + a
  dedicated test ("search without user filter must fail or
  return 0 results") mitigate this at Phase 4 implementation.

### Risks

- **Cognito JWT claim drift**: if Cognito changes how `sub` or
  `custom:tenant_id` are emitted, the filter + collection-name
  builder break at query time. Mitigation: wrap claim extraction
  in a small `Principal` struct (gateway already has this per
  Phase 2 A2) and fail hard if claims are missing, not silently
  fall through to unfiltered queries.

## Related

- ADR-0001 CreateMeeting + session-token issuance (Cognito JWT
  source)
- ADR-0019 RAG corpus + multilingual embedding pipeline (what gets
  stored)
- ADR-0020 Engine owns inference (seed + query both run in the
  engine process)
- `docs/threat-model.md` §Open Items (the Known Gap tracking this
  ADR's Phase 4 implementation dependency)
- Phase 2 Known Gaps "Cognito JWT middleware is stubbed, not
  production-wired" (the precondition that gates ADR-0022
  implementation)
