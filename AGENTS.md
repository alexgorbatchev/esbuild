---
created_on: 2026-10-03 09:05
last_modified: 2026-10-06 06:25
status: current
---

# Native compiler source

Fork of esbuild v0.28.1 with observed input, source-map and per-file define hooks. The user explicitly requests transferring every maintained compiler change into their GitHub fork and consuming it from dotfiles.

## Commands

- Native suites: `go test -race -count=1 ./internal/... ./pkg/...`
- Compiler coverage: `go test -race -count=1 -coverpkg=./internal/...,./pkg/... -coverprofile=.tmp/compiler-coverage.out ./internal/... ./pkg/...`

## Boundaries

- Preserve all 181 transferred source/license/test-asset/provenance files and the complete upstream repository. The pinned origin and exact native hook inventory are in [UPSTREAM.md](UPSTREAM.md).
- Every maintained compiler package has the unchanged minimum 90% statement coverage requirement. Include all positive-denominator packages; no directory/package/scripts exclusion or constant-only tests.
- Keep nil input/default hooks behavior grounded against upstream tests; real virtual/native evaluator parity, observed metadata/symlink/ZIP inputs, ignored author maps with generated output maps, denied-read errors and no writes need behavioral and disable/restore proof.
- Native resolver/parser metadata owns TypeScript configuration and lowering. Do not replace it with public plugin-field replay, a handwritten compiler-option map, source-string identifier rewrites or an intermediate transform that drops original module semantics.
- Automatically record new instructions in the appropriate AGENTS.md. The user's explicit fork migration authorizes creating this fork, moving all existing compiler changes and tests, committing and pushing the completed patch, and consuming an immutable remote revision. Ask first for another source version, API hook, dependency, workflow or publishing change beyond the transferred existing Goja test dependency and Go 1.26 test requirement.
- Never publish releases, tags, packages or production deployments without explicit authorization. Preserve upstream CLI behavior, module identity and licenses; do not rewrite the upstream CLI or workflows as part of this migration.
- Correctness repairs to the retained compiler's existing lifecycle/location contracts are within the approved local integration. Keep `pkg/api/serve_other.go` and `internal/logger/logger.go` unchanged until the coordinator confirms the precise reviewed patch and serialized source handoff. Record each accepted repair separately from the original hook/import inventory in UPSTREAM.md, with its actual native red/green and causal restoration evidence. This permits no new hook, API, source version or dependency.
- The coordinator confirms the exact logger V2 correction SHA256 `168a8681799fb11940657eb62a75922573507edf324e26cba5aed1271e6d16ad` and four frozen FS/logger test transfers, plus lifecycle V2 correction SHA256 `ad3ff3d06767deba4f7c19539ef7258ce64b318fff63e125c8c4f5bbff4a1e61` and three frozen API tests. Main owns both corrected default sources. Measure them directly because Go 1.26.2 coverage instrumentation does not establish overlay-backed corrected source. Preserve native path, cooked-string, public PnP diagnostics and actual listener/HTTP stream lifetimes; injected Windows path models do not enable a native Windows backend. The disjoint compiler helpers own only their explicitly reserved new tests; copied historical support remains read-only.

## Ownership and maintenance

The coordinator confirms four frozen behavioral test transfers in `internal/ast`, `internal/config`, `internal/css_ast` and `internal/helpers`, plus exact clone patch `497bc8216a412bc6ce0f0f95c1d04d864edef31c4cc4b336287d5a00c0d3bc9e` and quote patch `e37f50fe512aea97a4d8d585eb5e025b72d740692fed019b3c7fe5d50e603c01`. Preserve pseudo-class clone ownership and native JavaScript delimiter escaping without a new API or syntax. Main owns these default sources and must execute the exact transferred final bytes before accepting coverage; the helper retains `internal/test`, resolver and bundler tests until separate handover.

The coordinator confirms the exact native signed-VLQ correction, canonical patch SHA256 `8fff646374e5aac04ac0cbf250b0a6663d10e1cb17a4f177ed808ba995a4394d`, corrected `internal/sourcemap/sourcemap.go` SHA256 `f22e5f9cde95f8d52aeb9779c08cf1c26c69a457e11e1deca87c156a4e1f4527`, and two frozen xxhash/sourcemap behavioral tests. Preserve signed-32-bit source-map values, ASCII Base64 input and overflow rejection through the existing native producer/parser. Signatures and trusted generated-byte preconditions stay unchanged. Main owns source and fresh default-source coverage; helper overlay hits and historical denominators are never merged into that measurement.

The coordinator also authorizes only the reviewed `pkg/api/api_impl.go` disposal-order repair, corrected source SHA256 `c72eab86339b00fc41de99ba384e02eb0ce6e370c54ffa16b0099a5e5dfe6c30`: stop the retained HTTP handler before joining the watcher, then retain the existing active-build join. The additional combined Watch/event-stream test and its completed pre-rejection scratch proofs remain parked read-only in the helper checkout. Automatic approval review rejected that helper turn; do not retry, disguise, transfer or replay the rejected test. Existing maintained routine API/Context tests validate this source intake separately, without claiming complete combined-case acceptance.

The fork maintainer owns these runtime packages and native patches. Compiler updates preserve licensing, compare native hook changes against the exact new upstream source, rerun upstream/integration/causal tests and establish the full package coverage gate before acceptance. Runtime changes require a corresponding behavioral test change. Test filesystem adapters must use real native storage without a dotfiles dependency cycle or host fallback; native scratch output belongs in this repository's `.tmp` directory.
