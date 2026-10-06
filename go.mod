module github.com/evanw/esbuild

// The transferred catalog runtime tests and native filesystem fixtures require
// Go 1.26. Upstream version metadata and module identity remain unchanged.
go 1.26

// The existing Goja runtime test dependency selects this x/sys revision through
// x/text. Preserve the native module graph when updating this fork.
require golang.org/x/sys v0.0.0-20220722155257-8c9f86f7a55f

require github.com/dop251/goja v0.0.0-20260618133527-c9b2ea77db59

require (
	github.com/dlclark/regexp2/v2 v2.2.1 // indirect
	github.com/go-sourcemap/sourcemap v2.1.3+incompatible // indirect
	github.com/google/pprof v0.0.0-20230207041349-798e818bf904 // indirect
	golang.org/x/text v0.3.8 // indirect
)
