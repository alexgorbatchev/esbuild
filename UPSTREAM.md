---
created_on: 2026-10-03 09:05
last_modified: 2026-10-06 06:25
status: current
---

# Retained native compiler provenance

This is the catalog-planning branch of [alexgorbatchev/esbuild](https://github.com/alexgorbatchev/esbuild), forked from evanw/esbuild. Its upstream baseline is esbuild v0.28.1, Git commit `bb9db84c02433fbe37b3509f53f9f3e3cc48725e`. It preserves the full upstream repository and transfers all 181 maintained compiler source, license, test-asset and provenance files from dotfiles commit `db290f72e425be68ae1984b2c1262d2867e65204`.

Go import specifications and the module retain canonical `github.com/evanw/esbuild` identity. Dotfiles consumers select the immutable fork revision through Go's native module replacement mechanism. Upstream source strings, version metadata and existing command behavior are retained.

Two transferred filesystem test files now mount a separate real native directory through `os.Root` instead of importing dotfiles' in-memory filesystem. Their missing-input, host-isolation, range-read, injected modification-key and ZIP assertions are retained. The three native scratch-directory helpers use the fork-local `.tmp` directory. The Goja cooked-string test keeps the existing exact dependency version `v0.0.0-20260618133527-c9b2ea77db59`; modern tests require Go 1.26. These portability changes do not add a compiler hook or a dependency on dotfiles.

Preserve upstream MIT [LICENSE.md](LICENSE.md), copyright Evan Wallace, SHA256 `b40ec5baec7bb34fa5b1c09521fa3cd52d5fad7adafed74932a2010d3612a681`; nested xxhash [LICENSE.txt](internal/xxhash/LICENSE.txt), SHA256 `f566a9f97bacdaf00d9f21dd991e81dc11201c4e016c86b470799429a1c9a79c`; and existing source copyright headers, including filepath.go's BSD notice.

## Native patch inventory

| Path                        | Retained extension                                                                                                                                                        |
| --------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| pkg/api/api.go              | ReadOnlyFileSystem alias; BuildOptions InputFS, IgnoreInputSourceMaps, DefineForFile                                                                                      |
| pkg/api/api_impl.go         | Forward input filesystem through contexts/rebuild/plugin resolution; validate per-file native defines; reject injected-FS output writes/watch and injected object defines |
| internal/fs/fs.go           | Read-only input contract: ReadFile, ReadDir, Lstat, Readlink                                                                                                              |
| internal/fs/fs_input.go     | Forward native path metadata/readlink through supplied input                                                                                                              |
| internal/fs/filepath.go     | Observe native symlink traversal                                                                                                                                          |
| internal/fs/fs_real.go      | Native input reads/metadata/cache keys and explicit absolute working directory; no host fallback for supplied input                                                       |
| internal/fs/fs_zip.go       | Use archive/zip.NewReader over observed archive bytes; ordinary native ZIP route retained                                                                                 |
| internal/config/config.go   | Native per-file defines and authored-map policy                                                                                                                           |
| internal/bundler/bundler.go | Apply defines alongside native per-module resolution metadata; ignore input maps without discarding generated output maps                                                 |

## Retained correctness repairs

`internal/logger/logger.go` also carries the separately reviewed V2 correction, patch SHA256 `168a8681799fb11940657eb62a75922573507edf324e26cba5aed1271e6d16ad`, corrected source SHA256 `503f4910a3231ea682732a14489ff0c231e1874bb973e571eeba228479e3b988`. Native lexical paths include lowercase `z` drive roots. Embedded-source location mapping selects the new compressed run at its exact start and advances across complete validated hexadecimal, Unicode, surrogate-pair and legacy-octal escape tokens. These are repairs to existing path/location behavior, separate from the native hooks and import relocation; they add no parser syntax, public API, hook, dependency or source version.

`pkg/api/serve_other.go` carries the separately reviewed lifecycle V2 correction, patch SHA256 `ad3ff3d06767deba4f7c19539ef7258ce64b318fff63e125c8c4f5bbff4a1e61`, corrected source SHA256 `f7bc7594c3fd4f952d6d590f86858618c3d4da955efa8fab5eca08340a931872`. The HTTP handler owns event-stream writes until its return, uses native request-context cancellation and checked ResponseController.Flush, and signals stream termination before mutex cleanup so concurrent broadcast delivery can stop. Dispose joins captured stream termination; failed TLS startup closes the retained listener and joins the serve goroutine. Native race and failed-start listener regressions plus compiled causal mutations ground these existing lifecycle repairs. They add no public API, hook, dependency, source version or task.

Maintained FS/logger/API tests include exact native filesystem lifetime behavior, native-runtime cooked-string vectors, a public `api.Build` Yarn PnP consumer that checks original-source diagnostics and real Context Serve/Watch/HTTP/error/output lifetimes. Preserve those consumers and actual compiled causal failures when updating the retained implementation. Measure corrected default source directly; a passing runtime overlay does not prove that Go coverage instruments the same backing source.

`pkg/api/api_impl.go` additionally stops the retained HTTP handler before joining the watcher, preserving the disposal guard and active-build join. Corrected source SHA256 is `c72eab86339b00fc41de99ba384e02eb0ce6e370c54ffa16b0099a5e5dfe6c30`. The native combined-case failure and completed scratch correction proof precede an automatic approval-review rejection of the helper turn. They remain diagnostic evidence in that checkout; its new test is not transferred or replayed. This source-only intake uses the separately reviewed ordering delta and existing maintained API/Context regressions. Combined-case handover and final acceptance remain incomplete.

`internal/sourcemap/sourcemap.go` carries the reviewed signed-VLQ correction, canonical patch SHA256 `8fff646374e5aac04ac0cbf250b0a6663d10e1cb17a4f177ed808ba995a4394d`, corrected source SHA256 `f22e5f9cde95f8d52aeb9779c08cf1c26c69a457e11e1deca87c156a4e1f4527`. Existing native encoder/decoders use unsigned sign/magnitude arithmetic, support the minimum signed-32-bit value and reject non-ASCII/overflow UTF16 input before narrowing. These repairs follow [ECMA-426 sections 6.1–6.2](https://tc39.es/ecma426/) without adding a codec, parser API, hook, dependency or source version. Actual native producer/parser roundtrips, malformed input and fourteen compiled causal failures are retained in the helper's frozen numeric checkpoint. Maintained xxhash/sourcemap tests are transferred; corrected default-source measurement remains separate from overlay proof and old profiles.

## Update procedure

`internal/css_ast/css_ast.go` carries the reviewed pseudo-class clone correction, patch SHA256 `497bc8216a412bc6ce0f0f95c1d04d864edef31c4cc4b336287d5a00c0d3bc9e`, corrected source SHA256 `0349f67c4cc9306daf8854bd26e4034dedd567e6dcfb18e86289009325eb281a`. Cloned arguments belong to the clone; neither cloning nor subsequent clone edits mutate the original receiver. `internal/helpers/quote.go` carries quote patch SHA256 `e37f50fe512aea97a4d8d585eb5e025b72d740692fed019b3c7fe5d50e603c01`, corrected source SHA256 `64f55b77b48770661780ae0cf1479fb55d9ef19fbd0da1640be1c4211e6ba900`: the existing delimiter determines which ASCII quote requires escaping. Real CSS clone consumers and existing Goja string decoding ground these private correctness repairs, separately from hooks/import relocation. The four exact transferred AST/config/CSS/helpers tests require fresh default-source execution; the final keyed CSS fixture postdates the helper's restored race. No new API, syntax, dependency, version or coverage exemption is introduced.

Before changing this source, inspect the selected upstream version/module/license and compare these exact hooks against it. Preserve the maintained source scope and canonical imports. Exercise ordinary nil-hook behavior and native injected input/configuration parity with real evaluator results, including metadata inheritance, aliases/packages, symlink/ZIP input, original TypeScript maps, denied reads/output writes and cancellation. Temporarily disable the hooks and require compiled behavioral failures after restoration. Measure every maintained package against the unchanged 90% gate and run the installer full check after updating its immutable dependency.

The retained upstream suite and installer integration tests are actual behavior evidence. Historical profiles and overlay proofs do not establish current fork coverage or acceptance. Moving compiler packages out of dotfiles supplies no exemption from the 90% requirement. No npm package, executable release or tag publication is authorized by this source migration.

## Extraction verification checkpoint

The extracted full upstream Go suite passes with the race detector on Go 1.26.2 (`go test -race -count=1 -timeout=8m -coverpkg=./internal/...,./pkg/... -coverprofile=.tmp/native/compiler-coverage.out ./...`). The native source filesystem and ZIP assertions fail when supplied input dispatch is temporarily disabled, then pass after exact restoration together with the new real mounted-filesystem test.

This is a tested migration checkpoint. The native merged coverage profile places 24 of the 26 retained positive-denominator packages at or above90%; resolver is1939/2161 (89.726978%) and pkg/api is1501/1678 (89.451728%). Full upstream pkg/cli also measures0/745 and is outside the retained donor inventory. These are recorded deficits, not coverage waivers. Consumer-side per-file define and authored-input-map hook tests still require execution from dotfiles against the immutable remote fork. Neither this checkpoint nor source publication declares issue197 complete.
