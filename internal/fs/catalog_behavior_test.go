package fs

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func catalogNativeDirectory(t *testing.T) string {
	t.Helper()
	scratch, err := filepath.Abs("../../.tmp/compiler-fslogger-component")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(scratch, 0755); err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(scratch, "native-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Error(err)
		}
	})
	return directory
}

func catalogWrite(t *testing.T, name, contents string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func catalogRealFS(t *testing.T, options RealFSOptions) FS {
	t.Helper()
	result, err := RealFS(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, archive := range result.(*zipFS).zipFiles {
			if archive.nativeReader != nil {
				if err := archive.nativeReader.Close(); err != nil {
					t.Error(err)
				}
			}
		}
	})
	return result
}

func TestCatalogNativeEntriesRangesAndSymlinkLifetime(t *testing.T) {
	directory := catalogNativeDirectory(t)
	name := filepath.Join(directory, "Entry.ts")
	contents := "first\npayload"
	catalogWrite(t, name, contents)
	if err := os.Mkdir(filepath.Join(directory, "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{"entry-link": "Entry.ts", "dir-link": filepath.Join(directory, "nested"), "broken": "missing", "loop": "loop"} {
		if err := os.Symlink(target, filepath.Join(directory, link)); err != nil {
			t.Fatal(err)
		}
	}
	input := catalogRealFS(t, RealFSOptions{AbsWorkingDir: directory, DoNotCache: true})
	entries, canonical, original := input.ReadDirectory(directory)
	if canonical != nil || original != nil {
		t.Fatalf("read directory: %v / %v", canonical, original)
	}
	entry, different := entries.Get("entry.TS")
	if entry == nil || different == nil || different.Query != "entry.TS" || different.Actual != "Entry.ts" || different.Dir != directory {
		t.Fatalf("case-aware lookup lost original entry: %v / %v", entry, different)
	}
	if entry.Kind(input) != FileEntry || entry.Symlink(input) != "" {
		t.Fatal("ordinary native file was classified as a link or directory")
	}
	for _, test := range []struct {
		name   string
		kind   EntryKind
		target string
	}{
		{"entry-link", FileEntry, name},
		{"dir-link", DirEntry, filepath.Join(directory, "nested")},
		{"broken", 0, ""},
		{"loop", 0, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry, _ := entries.Get(test.name)
			if entry == nil || entry.Symlink(input) != test.target || entry.Kind(input) != test.kind {
				t.Fatalf("native entry classification differs: %v", entry)
			}
		})
	}
	if entry, different := entries.Get("absent"); entry != nil || different != nil {
		t.Fatal("missing directory member acquired a synthetic entry")
	}
	wantKeys := []string{"Entry.ts", "broken", "dir-link", "entry-link", "loop", "nested"}
	if !reflect.DeepEqual(entries.SortedKeys(), wantKeys) || entries.PeekEntryCount() != len(wantKeys) {
		t.Fatalf("native listing lost original spelling/order: %v", entries.SortedKeys())
	}
	var readers sync.WaitGroup
	for range 12 {
		readers.Go(func() {
			entry, _ := entries.Get("entry-link")
			if entry.Kind(input) != FileEntry || entry.Symlink(input) != name {
				t.Error("concurrent lazy metadata changed its native target")
			}
		})
	}
	readers.Wait()
	opened, canonical, original := input.OpenFile(name)
	if canonical != nil || original != nil || opened.Len() != len(contents) {
		t.Fatalf("open native file: %v / %v", canonical, original)
	}
	catalogCloseOpened(t, opened)
	for _, bounds := range [][2]int{{0, len(contents)}, {2, 7}, {5, 5}} {
		got, err := opened.Read(bounds[0], bounds[1])
		if err != nil || string(got) != contents[bounds[0]:bounds[1]] {
			t.Fatalf("native range %v = %q, %v", bounds, got, err)
		}
	}
	if _, err := opened.Read(0, len(contents)+1); !errors.Is(err, io.EOF) {
		t.Fatalf("read beyond native EOF: %v", err)
	}
	if err := os.Rename(name, name+".retained"); err != nil {
		t.Fatal(err)
	}
	catalogWrite(t, name, "replacement")
	got, err := opened.Read(0, len(contents))
	if err != nil || string(got) != contents {
		t.Fatalf("retained descriptor switched to replacement path: %q / %v", got, err)
	}
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := opened.Read(0, 1); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed descriptor remained readable: %v", err)
	}
	if err := opened.Close(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("second close hid descriptor error: %v", err)
	}
	for _, operation := range []string{"read", "open", "directory"} {
		t.Run(operation+" missing", func(t *testing.T) {
			missing := filepath.Join(directory, "missing")
			var canonical, original error
			switch operation {
			case "read":
				_, canonical, original = input.ReadFile(missing)
			case "open":
				_, canonical, original = input.OpenFile(missing)
			case "directory":
				_, canonical, original = input.ReadDirectory(missing)
			}
			if !errors.Is(canonical, syscall.ENOENT) || !errors.Is(original, os.ErrNotExist) {
				t.Fatalf("missing native path errors: %v / %v", canonical, original)
			}
		})
	}
	if resolved, ok := input.EvalSymlinks(filepath.Join(directory, "entry-link")); !ok || resolved != name {
		t.Fatalf("native symlink evaluation: %q / %v", resolved, ok)
	}
	if _, ok := input.EvalSymlinks(filepath.Join(name, "child")); ok {
		t.Fatal("ordinary file became a traversal directory")
	}
	if _, ok := input.EvalSymlinks(filepath.Join(directory, "loop")); ok {
		t.Fatal("symlink cycle was accepted")
	}
}

func TestCatalogNativeDirectoryCacheAndCreation(t *testing.T) {
	directory := catalogNativeDirectory(t)
	catalogWrite(t, filepath.Join(directory, "old"), "old")
	for _, cache := range []bool{true, false} {
		t.Run(map[bool]string{true: "cached", false: "fresh"}[cache], func(t *testing.T) {
			input := catalogRealFS(t, RealFSOptions{AbsWorkingDir: directory, DoNotCache: !cache})
			first, err, _ := input.ReadDirectory(directory)
			if err != nil {
				t.Fatal(err)
			}
			newName := filepath.Join(directory, "new")
			catalogWrite(t, newName, "new")
			second, err, _ := input.ReadDirectory(directory)
			entry, _ := second.Get("new")
			if err != nil || (entry == nil) != cache {
				t.Fatalf("directory cache ignored selected policy: %v / %v", entry, err)
			}
			if first.PeekEntryCount() != 1 {
				t.Fatal("previous immutable listing changed after native write")
			}
			if err := os.Remove(newName); err != nil {
				t.Fatal(err)
			}
		})
	}
	input := catalogRealFS(t, RealFSOptions{AbsWorkingDir: directory})
	nested := filepath.Join(directory, "parent", "child")
	if err := MkdirAll(input, nested+string(filepath.Separator)+".", 0755); err != nil {
		t.Fatal(err)
	}
	if err := MkdirAll(input, nested, 0755); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(nested); err != nil || !info.IsDir() {
		t.Fatalf("recursive native directory creation: %v / %v", info, err)
	}
	if err := MkdirAll(input, filepath.Join(directory, "old", "blocked"), 0755); !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("file parent was replaced while creating directory: %v", err)
	}
}

func TestCatalogNativeWatchTracksObservedMembershipAndFileImages(t *testing.T) {
	for _, observation := range []string{"individual", "all", "missing directory", "unusable key", "stable key", "missing file"} {
		t.Run(observation, func(t *testing.T) {
			directory := catalogNativeDirectory(t)
			name := filepath.Join(directory, "observed")
			input := catalogRealFS(t, RealFSOptions{AbsWorkingDir: directory, WantWatchData: true})
			watched := directory
			switch observation {
			case "individual", "all":
				catalogWrite(t, name, "old")
				entries, err, _ := input.ReadDirectory(directory)
				if err != nil {
					t.Fatal(err)
				}
				if observation == "all" {
					entries.SortedKeys()
				} else {
					entries.Get("observed")
					entries.PeekEntryCount()
				}
			case "missing directory":
				watched = filepath.Join(directory, "absent")
				if _, err, _ := input.ReadDirectory(watched); err == nil {
					t.Fatal("initial absent directory was readable")
				}
			case "missing file":
				watched = name
				if _, err, _ := input.ReadFile(name); err == nil {
					t.Fatal("initial absent file was readable")
				}
			default:
				watched = name
				catalogWrite(t, name, "old")
				if observation == "stable key" {
					old := time.Now().Add(-10 * time.Second)
					if err := os.Chtimes(name, old, old); err != nil {
						t.Fatal(err)
					}
				}
				if _, err, _ := input.ReadFile(name); err != nil {
					t.Fatal(err)
				}
			}
			check := input.WatchData().Paths[watched]
			if check == nil || check() != "" {
				t.Fatal("watch considered its unchanged observation dirty")
			}
			if observation == "individual" {
				catalogWrite(t, filepath.Join(directory, "unrelated"), "unrelated")
				if check() != "" {
					t.Fatal("unobserved membership invalidated an individual lookup")
				}
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
			} else if observation == "missing directory" {
				if err := os.Mkdir(watched, 0755); err != nil {
					t.Fatal(err)
				}
			} else if observation == "all" {
				catalogWrite(t, filepath.Join(directory, "additional"), "new")
			} else {
				catalogWrite(t, name, "changed contents")
			}
			if changed := check(); changed != watched {
				t.Fatalf("observed native mutation escaped watch: %q, want %q", changed, watched)
			}
		})
	}
}

func TestCatalogInjectedInputsCannotReadHostOrEnableWatch(t *testing.T) {
	directory := catalogNativeDirectory(t)
	name := filepath.Join(directory, "input.ts")
	catalogWrite(t, name, "native host bytes")
	virtual := catalogNewInjectedFS(t, directory)
	if err := virtual.MkdirAll(directory, 0755); err != nil {
		t.Fatal(err)
	}
	for _, options := range []RealFSOptions{{InputFS: virtual}, {InputFS: virtual, AbsWorkingDir: "relative"}, {InputFS: virtual, AbsWorkingDir: directory, WantWatchData: true}} {
		if _, err := RealFS(options); err == nil {
			t.Fatal("injected input accepted an invalid working context or watch mode")
		}
	}
	input := catalogRealFS(t, RealFSOptions{InputFS: virtual, AbsWorkingDir: directory})
	if _, err, original := input.ReadFile(name); !errors.Is(err, syscall.ENOENT) || !errors.Is(original, os.ErrNotExist) {
		t.Fatalf("missing injected file fell through to existing host: %v / %v", err, original)
	}
	if err := virtual.WriteFile(name, []byte("virtual selected bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	opened, err, original := input.OpenFile(name)
	if err != nil || original != nil {
		t.Fatalf("open injected file: %v / %v", err, original)
	}
	if got, err := opened.Read(8, opened.Len()); err != nil || string(got) != "selected bytes" {
		t.Fatalf("injected range read used host bytes: %q / %v", got, err)
	}
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := input.ModKey(name); !errors.Is(err, modKeyUnusable) {
		t.Fatalf("virtual input acquired native modification authority: %v", err)
	}
	if got, err := os.ReadFile(name); err != nil || string(got) != "native host bytes" {
		t.Fatalf("compiler input wrote native destination: %q / %v", got, err)
	}
}

func TestCatalogNativeZipAndYarnVirtualReads(t *testing.T) {
	directory := catalogNativeDirectory(t)
	archive := filepath.Join(directory, "package.zip")
	catalogWrite(t, archive, catalogZIPContents(t, map[string]string{"pkg/Entry.js": "export const zip = 1", "pkg/nested/value.txt": "nested", "empty/": ""}))
	input := catalogRealFS(t, RealFSOptions{AbsWorkingDir: directory})
	for _, name := range []string{archive + "/pkg/Entry.js", archive + "/PKG/entry.JS"} {
		for range 2 {
			contents, err, original := input.ReadFile(name)
			if err != nil || original != nil || contents != "export const zip = 1" {
				t.Fatalf("archive read %s: %q / %v / %v", name, contents, err, original)
			}
		}
	}
	for _, path := range []string{archive, archive + "/pkg", archive + "/pkg/"} {
		for range 2 {
			entries, err, original := input.ReadDirectory(path)
			if err != nil || original != nil || len(entries.SortedKeys()) == 0 {
				t.Fatalf("archive directory %s: %v / %v", path, err, original)
			}
		}
	}
	if _, err, original := input.ReadFile(archive + "/absent"); !errors.Is(err, syscall.ENOENT) || !errors.Is(original, syscall.ENOENT) {
		t.Fatalf("missing ZIP member acquired contents: %v / %v", err, original)
	}
	if _, err, _ := input.ReadDirectory(archive + "/absent"); !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("missing ZIP directory acquired entries: %v", err)
	}
	virtual := filepath.Join(directory, "__virtual__", "hash", "0", "package.zip", "pkg", "Entry.js")
	if contents, err, _ := input.ReadFile(virtual); err != nil || contents != "export const zip = 1" {
		t.Fatalf("Yarn virtual native archive mapping: %q / %v", contents, err)
	}
	if dir := input.Dir(filepath.Join(directory, "__virtual__", "hash", "0")); dir != directory {
		t.Fatalf("virtual-root parent exposes synthetic segments: %q", dir)
	}
	for _, path := range []string{"plain/path", "__virtual__/hash/nope/file", "$$virtual/hash", "__virtual__/hash/99999999999999999999999/file"} {
		if _, _, ok := ParseYarnPnPVirtualPath(path); ok {
			t.Fatalf("malformed virtual path was interpreted: %q", path)
		}
	}
}

func TestCatalogInjectedLexicalPathSemantics(t *testing.T) {
	for _, target := range []struct {
		name, cwd string
		windows   bool
		separator byte
	}{
		{"unix", "/workspace", false, '/'},
		{"windows", `C:\workspace`, true, '\\'},
	} {
		t.Run(target.name, func(t *testing.T) {
			fp := goFilepath{cwd: target.cwd, isWindows: target.windows, pathSeparator: target.separator}
			for _, parts := range [][]string{{"", ""}, {".", "asset", "..", "input.ts"}, {"dir", "", "nested", "..", "input.ts"}} {
				joined := fp.join(parts)
				if joined != "" && fp.clean(joined) != joined {
					t.Fatalf("joining paths produced an unclean spelling: %q", joined)
				}
				if joined != "" {
					if fp.base(joined) != "input.ts" || fp.ext(joined) != ".ts" {
						t.Fatalf("joined file lost basename/extension: %q", joined)
					}
				}
			}
			absolute, err := fp.abs("assets/input.ts")
			if err != nil || !fp.isAbs(absolute) {
				t.Fatalf("absolute lexical path: %q / %v", absolute, err)
			}
			relative, err := fp.rel(target.cwd, absolute)
			if err != nil || fp.join([]string{target.cwd, relative}) != absolute {
				t.Fatalf("lexical relative/absolute round-trip: %q / %v", relative, err)
			}
			if _, err := fp.rel("../base", "other"); err == nil {
				t.Fatal("relative path discarded unknown parent authority")
			}
		})
	}
	fp := goFilepath{isWindows: true, pathSeparator: '\\', cwd: `C:\workspace`}
	for _, test := range []struct{ path, clean, volume string }{
		{`C:`, `C:.`, `C:`}, {`C:/a/../b//`, `C:\b`, `C:`},
		{`\\server\share\a\..\b`, `\\server\share\b`, `\\server\share`},
		{`\\server\share`, `\\server\share`, `\\server\share`},
		{`..\..\a\..\b`, `..\..\b`, ""}, {`\..\..\b`, `\b`, ""},
	} {
		if got := fp.clean(test.path); got != test.clean || fp.volumeName(test.path) != test.volume {
			t.Fatalf("Windows lexical cleanup %q: %q / %q", test.path, got, fp.volumeName(test.path))
		}
	}
	for _, test := range []struct {
		parts []string
		want  string
	}{{[]string{`C:`, "", "child"}, `C:child`}, {[]string{`\`, `server`, `share`}, `\server\share`}, {[]string{`\\server\share`, "child"}, `\\server\share\child`}} {
		if got := fp.join(test.parts); got != test.want {
			t.Fatalf("joining manufactured or lost UNC/drive semantics: %q, want %q", got, test.want)
		}
	}
	if _, err := fp.rel(`C:\a`, `D:\a`); err == nil {
		t.Fatal("relative path crossed independent drive volumes")
	}
	for _, path := range []string{"", `a`, `1:`, `\\.\share`, `\\server\\share`} {
		if volume := fp.volumeName(path); volume != "" {
			t.Fatalf("invalid lexical volume became an anchor: %q / %q", path, volume)
		}
	}
	if !fp.isAbs("nul") || fp.isAbs(`C:relative`) || fp.base("") != "." || fp.base(`C:\`) != `\` || fp.dir(`\\server\share`) != `\\server\share` {
		t.Fatal("drive/device/root lexical operations lost their defined semantics")
	}
	if !strings.HasSuffix(fp.fromSlash("a/b"), `a\b`) {
		t.Fatal("Windows separator conversion was omitted")
	}
}
