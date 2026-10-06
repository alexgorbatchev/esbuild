package fs

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func catalogZIPContents(t *testing.T, files map[string]string) string {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, name := range slices.Sorted(maps.Keys(files)) {
		file, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(file, files[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.String()
}

func catalogCloseOpened(t *testing.T, file OpenedFile) {
	t.Helper()
	t.Cleanup(func() {
		if err := file.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Error(err)
		}
	})
}

func TestCatalogRetainedCompilerModelFileAndPathRoundTrips(t *testing.T) {
	for _, target := range []struct {
		name, cwd, file string
		kind            MockKind
	}{
		{"unix model", "/work", "/work/assets/Entry.ts", MockUnix},
		{"windows model", `C:\work`, `C:\work\assets\Entry.ts`, MockWindows},
	} {
		t.Run(target.name, func(t *testing.T) {
			contents := "observed model bytes"
			input := MockFS(map[string]string{target.file: contents}, target.kind, target.cwd)
			absolute, ok := input.Abs(target.file)
			relative, relOK := input.Rel(input.Cwd(), absolute)
			if !ok || !relOK || !input.IsAbs(absolute) || input.Cwd() != target.cwd || input.Join(input.Cwd(), relative) != target.file {
				t.Fatalf("explicit target model lost file identity: %q / %q", absolute, relative)
			}
			if input.Join(input.Dir(absolute), input.Base(absolute)) != absolute || input.Ext(absolute) != ".ts" {
				t.Fatal("model directory/basename/extension no longer describe the readable file")
			}
			entries, canonical, original := input.ReadDirectory(input.Dir(absolute))
			entry, _ := entries.Get("entry.TS")
			if canonical != nil || original != nil || entry == nil || entry.Kind(input) != FileEntry || entry.Symlink(input) != "" {
				t.Fatalf("explicit target model lost file entry metadata: %v / %v", canonical, original)
			}
			opened, canonical, original := input.OpenFile(absolute)
			if canonical != nil || original != nil || opened.Len() != len(contents) {
				t.Fatalf("model file open: %v / %v", canonical, original)
			}
			catalogCloseOpened(t, opened)
			data, err := opened.Read(2, len(contents)-1)
			if err != nil || string(data) != contents[2:len(contents)-1] {
				t.Fatalf("model opened-file range changed original bytes: %q / %v", data, err)
			}
			if _, canonical, original := input.OpenFile(input.Join(target.cwd, "absent")); !errors.Is(canonical, syscall.ENOENT) || !errors.Is(original, syscall.ENOENT) {
				t.Fatalf("model invented a missing file: %v / %v", canonical, original)
			}
			if _, err := input.ModKey(absolute); err == nil {
				t.Fatal("explicit model acquired a native modification key")
			}
			if _, ok := input.EvalSymlinks(absolute); ok {
				t.Fatal("explicit model claimed native symlink resolution")
			}
			missing, _, _ := input.ReadDirectory(input.Join(target.cwd, "absent"))
			if missing.PeekEntryCount() != 0 || len(missing.SortedKeys()) != 0 {
				t.Fatal("missing model directory exposed entries")
			}
		})
	}
	empty := MakeEmptyDirEntries("/missing")
	if entry, _ := empty.Get("anything"); entry != nil || empty.PeekEntryCount() != 0 || len(empty.SortedKeys()) != 0 {
		t.Fatal("empty directory result exposed a file")
	}
}

func TestCatalogNativePathRoundTripsAndModificationKeys(t *testing.T) {
	directory := catalogNativeDirectory(t)
	input := catalogRealFS(t, RealFSOptions{AbsWorkingDir: directory})
	absolute, ok := input.Abs("assets/input.ts")
	relative, relOK := input.Rel(directory, absolute)
	if !ok || !relOK || !input.IsAbs(absolute) || input.Cwd() != directory || input.Join(directory, relative) != absolute || input.Base(absolute) != "input.ts" || input.Ext(absolute) != ".ts" {
		t.Fatalf("native compiler paths lost readable identity: %q / %q", absolute, relative)
	}
	if _, ok := input.Rel("../unknown", "other"); ok {
		t.Fatal("native relative path discarded unknown parents")
	}
	defaultInput := catalogRealFS(t, RealFSOptions{})
	workingDirectory, err := os.Getwd()
	if err != nil || defaultInput.Cwd() != workingDirectory {
		t.Fatalf("default compiler working directory: %q / %v", defaultInput.Cwd(), err)
	}
	for _, observation := range []string{"missing", "recent file", "stable file", "read before key"} {
		t.Run(observation, func(t *testing.T) {
			name := filepath.Join(directory, strings.ReplaceAll(observation, " ", "-"))
			input := catalogRealFS(t, RealFSOptions{AbsWorkingDir: directory, WantWatchData: true})
			if observation != "missing" {
				catalogWrite(t, name, "original")
			}
			if observation == "stable file" || observation == "read before key" {
				old := time.Now().Add(-10 * time.Second)
				if err := os.Chtimes(name, old, old); err != nil {
					t.Fatal(err)
				}
			}
			if observation == "read before key" {
				if _, err, _ := input.ReadFile(name); err != nil {
					t.Fatal(err)
				}
			}
			key, err := input.ModKey(name)
			if observation == "missing" && !errors.Is(err, os.ErrNotExist) || observation == "recent file" && !errors.Is(err, modKeyUnusable) || (observation == "stable file" || observation == "read before key") && err != nil {
				t.Fatalf("native modification-key capability: %+v / %v", key, err)
			}
			if observation == "recent file" {
				if _, err, _ := input.ReadFile(name); err != nil {
					t.Fatal(err)
				}
			}
			check := input.WatchData().Paths[name]
			if check == nil || check() != "" {
				t.Fatal("modification-key watch invalidated unchanged native input")
			}
			catalogWrite(t, name, "different contents")
			if changed := check(); changed != name {
				t.Fatalf("native modification-key watch missed replacement: %q", changed)
			}
		})
	}
}

func TestCatalogInjectedZIPReadsAndRejectedArchiveBytes(t *testing.T) {
	directory := catalogNativeDirectory(t)
	virtual := catalogNewInjectedFS(t, directory)
	if err := virtual.MkdirAll(directory, 0755); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(directory, "package.zip")
	contents := "export const observed = true"
	if err := virtual.WriteFile(archive, []byte(catalogZIPContents(t, map[string]string{"pkg/Entry.js": contents})), 0600); err != nil {
		t.Fatal(err)
	}
	input := catalogRealFS(t, RealFSOptions{AbsWorkingDir: directory, InputFS: virtual})
	data, canonical, original := input.ReadFile(archive + "/pkg/Entry.js")
	if canonical != nil || original != nil || data != contents {
		t.Fatalf("observed archive bytes were not used: %q / %v / %v", data, canonical, original)
	}
	if _, err := os.Stat(archive); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("injected ZIP created native input: %v", err)
	}
	for _, storage := range []string{"native", "injected"} {
		t.Run(storage, func(t *testing.T) {
			badArchive := filepath.Join(directory, storage+".zip")
			var input FS
			if storage == "native" {
				catalogWrite(t, badArchive, "invalid archive bytes")
				input = catalogRealFS(t, RealFSOptions{AbsWorkingDir: directory})
			} else {
				if err := virtual.WriteFile(badArchive, []byte("invalid archive bytes"), 0600); err != nil {
					t.Fatal(err)
				}
				input = catalogRealFS(t, RealFSOptions{AbsWorkingDir: directory, InputFS: virtual})
			}
			if data, canonical, original := input.ReadFile(badArchive + "/pkg/Entry.js"); data != "" || canonical == nil || original == nil {
				t.Fatalf("invalid archive exposed a member: %q / %v / %v", data, canonical, original)
			}
		})
	}
}
