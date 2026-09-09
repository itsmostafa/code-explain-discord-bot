# qi vs zvec-grep: feature audit

Date: 2026-09-04 (America/Los_Angeles).

Reviewed zvec-grep commit `52653951b24617762f4ab0c71c34d594e5001617` and qi working tree based on `0e1830d8fb7b663a3a5b2946616abf6ff4569938`, including existing uncommitted changes. Three Luna subagents investigated retrieval, indexing, and agent UX; the primary agent checked and consolidated findings. This is source inspection, not a measured performance comparison. The original audit performed no implementation or benchmark runs; the implementation follow-ups for findings 1 and 2 are recorded below. Existing user changes were preserved.

## Recommendation

Borrow zvec-grep’s evidence and operational contracts before its infrastructure. qi already has the core retrieval algorithms. Its most valuable improvements are making search results directly retrievable, making indexing recoverable, and exposing scope and freshness clearly. Keep the lightweight SQLite architecture until measurements justify changing it.

Priority labels below express proposed work order, not confirmed incident severity. “Small/medium/large” are relative scope estimates.

## 1. P1: Make every search hit directly retrievable and citable (medium)

**What zg gets right:** hits carry source ranges, files, and multiple evidence records. See [range types](https://github.com/zvec-ai/zvec-grep/blob/52653951b24617762f4ab0c71c34d594e5001617/src/engine/types.ts#L101) and [hit evidence](https://github.com/zvec-ai/zvec-grep/blob/52653951b24617762f4ab0c71c34d594e5001617/src/engine/types.ts#L329).

**qi gap:** `internal/search/types.go:4` returns integer document/chunk IDs, but `cmd/get.go:50` resolves a content-hash prefix. The search response does not supply that hash. There are no start/end line fields. `internal/chunker/breakpoint.go:135` copies the section ordinal into each chunk, so it cannot identify a later chunk’s exact location.

**Implement:** expose a full content hash as a public retrieval handle; include source start/end lines and a source identity/version. Preserve raw-source offsets through Markdown/frontmatter parsing rather than deriving them from transformed text. Keep document diversity, but optionally attach a bounded list of supporting passages to each document.

**Verify:** search JSON → get round trip; duplicate-content paths; several chunks under one heading; frontmatter, CRLF, Unicode, code fences, and files changed since indexing.

**Implementation follow-up:** search/query now expose `hash`, `source_uri`,
`start_line`, and `end_line`, with raw-source mappings carried through parsing
and chunking. `qi get <hash> --lines A:B` retrieves the indexed snapshot, not the
live file. `--passages 0–5` adds bounded supporting passages while retaining
one result and one ranking contribution per document per retriever. Existing
indexes require a normal `qi index <collection>` to rebuild missing ranges;
search rejects unknown ranges instead of inventing citations. The hash is a
source version, not proof of current filesystem freshness or a promise to
retain historical versions. `task check` passes, including search→get snapshot
round trips, duplicate-content paths, legacy-range repair, Markdown/CRLF/Unicode
source mapping, and bounded supporting evidence. Other findings below remain
proposals.

## 2. P1: Persist embedding progress between batches (medium)

**What zg gets right:** automatic failed-file retry and structured failure details ([index pipeline](https://github.com/zvec-ai/zvec-grep/blob/52653951b24617762f4ab0c71c34d594e5001617/src/engine/pipeline/indexing/index.ts#L248)).

**qi gap:** `internal/providers/embedding.go:90` already batches HTTP requests, but returns an error if a later batch fails. `internal/indexer/embedder.go:85` waits for the entire provider call before persisting vectors. Earlier successful network work is consequently lost on a late failure, and all pending texts/vectors are materialized together.

**Implement:** move the persistence boundary to bounded batches, preserve valid successes, retry transient errors with bounded backoff, and record failed chunk/path details. Reuse existing fingerprint and vector validation; qi already detects stale/missing/invalid embeddings and should retain that behavior.

**Verify:** fail the second batch, restart, and assert the first batch is not sent again; cover 429/5xx, cancellation, malformed vectors, and fingerprint changes.

**Implementation follow-up:** `EmbedCollection` now embeds and persists one
bounded batch at a time (`Embedder.BatchSize`, wired from the provider's
configured `batch_size` so the persistence boundary matches the request
boundary), so a failure late in a run never discards earlier network work and
a rerun only sends what is still missing. The provider marks 429, 5xx and
network errors outside a cancelled context with `providers.ErrTransient`; the
indexer retries only those, three attempts with doubling backoff, and stops
the run when they are exhausted. A batch the provider rejects permanently is recorded with the files
it came from and the run continues, so one bad batch cannot block every later
batch on every rerun; its chunks stay pending, so a rerun retries them. Retry
lives in the indexer, not the provider, so query-time embedding still falls
back to BM25 immediately instead of stalling. Cancellation is never treated as
transient, and a local database write failure stops the run rather than paying
for provider calls that cannot be stored. Existing fingerprint, validation and
stale-vector repair behavior is unchanged. `task check` passes, including
resume-without-resend, transient retry, provider-down stop, cancellation, and
per-status transient classification including a truncated response body.

## 3. P1: Add consistent ignore rules and query scope filters (medium)

**What zg gets right:** discovery supports ignore files, ordered glob rules, hidden-file controls and size/depth limits ([scanner](https://github.com/zvec-ai/zvec-grep/blob/52653951b24617762f4ab0c71c34d594e5001617/src/engine/pipeline/indexing/scanner/index.ts#L88)); indexed queries can narrow scope too ([pipeline guide](https://github.com/zvec-ai/zvec-grep/blob/52653951b24617762f4ab0c71c34d594e5001617/docs/04-pipeline.md)).

**qi gap:** `internal/indexer/indexer.go:100` builds an exact basename ignore set; lines 130–150 apply directory and extension checks. This is not gitignore/glob semantics. `internal/search/types.go:30` offers collection/date filters but no path or file-type constraints. The 10 MiB cap is fixed.

**Implement:** explicit include/exclude globs, gitignore-compatible discovery, configurable size limits, and path/type filters applied before retrieval limits in both BM25 and vector search. Define how policy changes remove now-excluded indexed files. Preserve qi’s secure symlink handling.

**Verify:** nested rules and negation, root dot-directories, exclude-to-include transitions, and relevant in-scope hits buried below out-of-scope candidates.

## 4. P1: Report operational status and freshness (medium; watcher later)

**What zg gets right:** an explicit status/readiness interface and freshness semantics, backed by a watcher with reconciliation ([watch manager](https://github.com/zvec-ai/zvec-grep/blob/52653951b24617762f4ab0c71c34d594e5001617/src/daemon/watch-manager.ts#L58), [execution modes](https://github.com/zvec-ai/zvec-grep/blob/52653951b24617762f4ab0c71c34d594e5001617/docs/06-server.md)).

**qi gap:** `cmd/doctor.go` checks installation, storage, paths, permissions and embedding health, but search results do not say whether they reflect current files. Updating requires manual indexing. Existing indexing errors and run information are useful foundations, not missing wholesale.

**Implement first:** `qi status --format json` with last successful indexing time, failed paths, embedding coverage and a recommended action. Distinguish “indexed at” from verified freshness; do not claim freshness from time alone. Add explicit refresh on demand. An opt-in watcher can follow once targeted incremental updates are robust.

**Verify:** edited/deleted files, failed reads preserving old content, interrupted indexing, and status distinguishing lexical readiness from semantic readiness.

## 5. P2: Add compact, bounded evidence output (small to medium)

zg offers selectable previews and compact agent output ([output contract](https://github.com/zvec-ai/zvec-grep/blob/52653951b24617762f4ab0c71c34d594e5001617/docs/04-pipeline.md#output-for-agents-and-people)). qi supports text/Markdown/JSON, but `internal/output/formatter.go` prints available snippets without a search-response byte budget; vector results carry entire chunk text (`internal/search/vector.go:66`).

Add `--preview none|short|full`, a response budget, explicit truncation, and a bounded context-expansion operation. Preserve IDs/ranges even when previews are omitted. Test multibyte truncation and many hits; measure tokens rather than assuming shorter-looking output is cheaper.

## 6. P2: Add multi-query retrieval and explicit exact verification (medium)

zg separates lexical/vector routes, can fuse multiple query groups, and offers managed ripgrep without an index ([route guide](https://github.com/zvec-ai/zvec-grep/blob/52653951b24617762f4ab0c71c34d594e5001617/docs/04-pipeline.md#querying)). qi joins positional arguments into one query (`cmd/query.go:29`) and always runs BM25 in hybrid mode (`internal/search/hybrid.go:31`).

Allow semantic intent plus separate exact lexical anchors, and optionally vector-only retrieval. For exhaustive verification, either document a direct ripgrep handoff using resolved paths or add an optional `qi grep` adapter; do not make ripgrep required for ordinary search. Mark ranked results as non-exhaustive and bounded exact results as truncated. Test repeated evidence not inflating RRF and explicit vector-only errors versus hybrid fallback.

## 7. P2: Improve candidate refill after deduplication (medium)

zg expands recall depth until enough candidates are found or a cap is reached ([adaptive recall](https://github.com/zvec-ai/zvec-grep/blob/52653951b24617762f4ab0c71c34d594e5001617/src/engine/pipeline/search/index.ts#L520)). qi retrieves a fixed pool (`internal/search/postprocess.go:93`) and then collapses duplicate snippets (`:79`). Although qi correctly counts distinct documents within retrievers, many different documents with identical snippets can still consume the pool and leave fewer final results.

Add bounded refill only when final deduplication leaves too few hits. Measure latency and recall; do not copy zg’s depth constants without evidence. Test duplicate-heavy corpora with unique matches beyond the first pool.

## 8. P2: Make agent setup discoverable (medium)

zg supports managed installation for several agent hosts and an MCP interface ([agent integrations](https://github.com/zvec-ai/zvec-grep/blob/52653951b24617762f4ab0c71c34d594e5001617/docs/01-agents.md)). qi already ships `skills/qi-cli/SKILL.md` and a Claude marketplace entry, so it does have agent integration.

First make the CLI contract reliable and document/install the skill for Codex as well as Claude. Add MCP if users need structured discovery or cannot run shell commands. Keep adapters thin over existing services and test real search/get invocation after installation; configuration written successfully is not proof the agent can call it.

## 9. P2: Clarify local versus remote embedding behavior (small)

zg explicitly documents remote grants and revocation ([embedding authorization](https://github.com/zvec-ai/zvec-grep/blob/52653951b24617762f4ab0c71c34d594e5001617/docs/07-embedding.md)). qi’s README says “Fully local + offline” while also supporting OpenAI, and indexing automatically embeds with the configured provider (`cmd/index.go:172`).

Clarify that local storage and lexical retrieval work offline, while remote providers receive embedding inputs. Show the configured destination during setup/status; consider a persisted remote-use policy for automation. Explicit provider configuration already conveys user intent: the absence of an extra prompt is not by itself a security vulnerability.

## 10. P2/P3: Expand formats selectively; preserve structure (medium to large)

zg extracts code symbols/signatures and uses metadata in embedding content ([vector content](https://github.com/zvec-ai/zvec-grep/blob/52653951b24617762f4ab0c71c34d594e5001617/src/engine/extraction/vector-content.ts#L68)). qi defaults to Markdown/plaintext and falls back to plaintext for unregistered extensions (`internal/parser/parser.go:32`).

Add useful text formats first, then Go/code structure only if repository search is a target workflow. Raw HTML/JSON text is not equivalent to a format-aware parser. PDF/Office support should not be credited as an existing zg advantage: its current pipeline explicitly skips those binary formats. Graph retrieval, native GUI, and mobile are roadmap items, not shipped parity requirements.

## 11. P2: Establish retrieval and agent-efficiency evaluations (medium)

zg includes reproducible paired agent evaluations ([benchmark protocol](https://github.com/zvec-ai/zvec-grep/blob/52653951b24617762f4ab0c71c34d594e5001617/benchmarks/README.md)). qi has correctness tests but no comparable checked-in quality/performance evaluation suite identified in this audit.

Create a held-out corpus with exact terms, paraphrases, dates, duplicate-heavy files, long documents, and multilingual queries. Track Recall@k, MRR, citation correctness, p50/p95 latency, indexing time, peak memory, and output tokens. Compare lexical/hybrid and each proposed change using the same corpus. Add small paired agent tasks after deterministic retrieval metrics exist. Published zg numbers do not establish that zg outperforms qi.

## 12. P3: Optimize vector search only after measuring scale (large for ANN)

zg uses HNSW ([storage schema](https://github.com/zvec-ai/zvec-grep/blob/52653951b24617762f4ab0c71c34d594e5001617/src/engine/storage/zvec.ts#L934)). qi loads candidate vectors and chunk text, computes cosine distances, and sorts all candidates (`internal/search/vector.go:82`). This has a clear scaling cost but is simple and exact.

Benchmark representative collection sizes. First consider retaining only the best chunk per document, bounded top-k selection, and loading text after candidate selection. Adopt an ANN backend only if latency warrants the recall/storage/dependency tradeoff; retain the CGo-free fallback.

## What qi should keep

- Optional embeddings and lexical fallback on provider failure (`internal/search/hybrid.go`).
- Unicode-aware lexical sanitization and AND-to-OR relaxation (`internal/search/bm25.go`).
- Document-level RRF, result diversity, and duplicate collapse (`internal/search/fusion.go`, `postprocess.go`).
- Date metadata and date filters; note newest-first operates over the retrieved pool, not every matching document.
- Content-addressed storage, secure file opening, embedding fingerprints and validation.
- A single SQLite database and lightweight CLI; a mandatory daemon would change the product’s operational cost.

## Suggested implementation sequence

1. Public retrieval handles, exact source ranges, and search→get tests. Done.
2. Batch embedding persistence/recovery (done) and structured status.
3. Shared discovery/query filters and bounded preview output.
4. Evaluation corpus; use it to decide multi-query/refill/ranking changes.
5. Broader agent setup; optional watch, code parsers, or ANN only with demonstrated demand.

The original audit proposed these changes without changing code or running
`task check`. Findings 1 and 2 now have implementation follow-ups above; the
remaining findings are still proposals.
