package fs

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// catalogInjectedFS mounts a separate native tree at the compiler's logical
// working directory. Reading it cannot fall through to the host fixture.
type catalogInjectedFS struct {
	root  *os.Root
	mount string
}

func catalogNewInjectedFS(t *testing.T, mount string) *catalogInjectedFS {
	t.Helper()
	root, err := os.OpenRoot(catalogNativeDirectory(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	})
	return &catalogInjectedFS{root: root, mount: mount}
}

func (f *catalogInjectedFS) path(name string) (string, error) {
	return filepath.Rel(f.mount, name)
}

func (f *catalogInjectedFS) ReadFile(name string) ([]byte, error) {
	relative, err := f.path(name)
	if err != nil {
		return nil, err
	}
	return f.root.ReadFile(relative)
}

func (f *catalogInjectedFS) ReadDir(name string) ([]string, error) {
	relative, err := f.path(name)
	if err != nil {
		return nil, err
	}
	directory, err := f.root.Open(relative)
	if err != nil {
		return nil, err
	}
	names, err := directory.Readdirnames(-1)
	err = errors.Join(err, directory.Close())
	slices.Sort(names)
	return names, err
}

func (f *catalogInjectedFS) Lstat(name string) (os.FileInfo, error) {
	relative, err := f.path(name)
	if err != nil {
		return nil, err
	}
	return f.root.Lstat(relative)
}

func (f *catalogInjectedFS) Readlink(name string) (string, error) {
	relative, err := f.path(name)
	if err != nil {
		return "", err
	}
	return f.root.Readlink(relative)
}

func (f *catalogInjectedFS) MkdirAll(name string, mode os.FileMode) error {
	relative, err := f.path(name)
	if err != nil {
		return err
	}
	return f.root.MkdirAll(relative, mode)
}

func (f *catalogInjectedFS) WriteFile(name string, data []byte, mode os.FileMode) error {
	relative, err := f.path(name)
	if err != nil {
		return err
	}
	return f.root.WriteFile(relative, data, mode)
}

func TestCatalogInjectedNativeMountIsolationAndMetadata(t *testing.T) {
	directory := catalogNativeDirectory(t)
	name := filepath.Join(directory, "entry.ts")
	catalogWrite(t, name, "host bytes")
	input := catalogNewInjectedFS(t, directory)
	if _, err := input.ReadFile(name); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty injected tree read host file: %v", err)
	}
	if err := input.WriteFile(name, []byte("injected bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := input.root.Symlink("entry.ts", "link"); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "link")
	if data, err := input.ReadFile(link); err != nil || string(data) != "injected bytes" {
		t.Fatalf("injected link selected host bytes: %q / %v", data, err)
	}
	if info, err := input.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("injected link lost native metadata: %v / %v", info, err)
	}
	if target, err := input.Readlink(link); err != nil || target != "entry.ts" {
		t.Fatalf("injected link target changed: %q / %v", target, err)
	}
	if names, err := input.ReadDir(directory); err != nil || !slices.Equal(names, []string{"entry.ts", "link"}) {
		t.Fatalf("injected directory selected host members: %v / %v", names, err)
	}
	if _, err := input.ReadFile(filepath.Join(directory, "..", "outside")); err == nil {
		t.Fatal("injected tree allowed parent traversal")
	}
	if data, err := os.ReadFile(name); err != nil || string(data) != "host bytes" {
		t.Fatalf("injected write changed host fixture: %q / %v", data, err)
	}
}
