package xxhash

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func catalogWriteDigest(t *testing.T, digest *Digest, contents []byte) {
	t.Helper()
	if n, err := digest.Write(contents); err != nil || n != len(contents) {
		t.Fatalf("digest write consumed %d/%d bytes: %v", n, len(contents), err)
	}
}

func TestCatalogXXH64NativeVectorsAcrossStreamingBoundaries(t *testing.T) {
	// Published vectors from cespare/xxhash v1.1.0's TestSum.
	for _, test := range []struct {
		input string
		want  uint64
	}{
		{"", 0xef46db3751d8e999}, {"a", 0xd24ec4f1a98c6e5b}, {"as", 0x1c330fb2d66be179},
		{"asd", 0x631c37ce72a97393}, {"asdf", 0x415872f599cea71e},
		{"Call me Ishmael. Some years ago--never mind how long precisely-", 0x02a2e85470d6fd96},
	} {
		t.Run(fmt.Sprintf("length-%d", len(test.input)), func(t *testing.T) {
			if got := Sum64([]byte(test.input)); got != test.want {
				t.Fatalf("native XXH64 vector = %016x, want %016x", got, test.want)
			}
			for _, chunkSize := range []int{1, 3, 7, 31, 32, 33, 64} {
				digest := New()
				for start := 0; start < len(test.input); start += chunkSize {
					catalogWriteDigest(t, digest, []byte(test.input[start:min(start+chunkSize, len(test.input))]))
				}
				catalogWriteDigest(t, digest, nil)
				if got := digest.Sum64(); got != test.want {
					t.Fatalf("streaming chunk %d = %016x, want vector %016x", chunkSize, got, test.want)
				}
				prefix := []byte("existing-prefix")
				appended := digest.Sum(bytes.Clone(prefix))
				if !bytes.Equal(appended[:len(prefix)], prefix) || len(appended) != len(prefix)+digest.Size() || binary.BigEndian.Uint64(appended[len(prefix):]) != test.want || digest.Sum64() != test.want {
					t.Fatal("Sum changed the streaming state, prefix or canonical digest byte order")
				}
			}
		})
	}
}

func TestCatalogDigestPersistsAndResumesNativeHashState(t *testing.T) {
	scratch, err := filepath.Abs("../../.tmp/compiler-fslogger-component")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(scratch, 0755); err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(scratch, "digest-state-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Error(err)
		}
	})
	contents := make([]byte, 257)
	for i := range contents {
		contents[i] = byte(i*29 + 7)
	}
	for _, prefixLength := range []int{0, 1, 7, 31, 32, 33, 63, 64, 127, len(contents)} {
		t.Run(fmt.Sprintf("prefix-%d", prefixLength), func(t *testing.T) {
			digest := New()
			catalogWriteDigest(t, digest, contents[:prefixLength])
			state, err := digest.MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(directory, "state.bin")
			if err := os.WriteFile(name, state, 0600); err != nil {
				t.Fatal(err)
			}
			persisted, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			restored := New()
			catalogWriteDigest(t, restored, []byte("obsolete in-memory state"))
			if err := restored.UnmarshalBinary(persisted); err != nil {
				t.Fatal(err)
			}
			clear(persisted)
			if restored.Sum64() != digest.Sum64() {
				t.Fatal("native persisted hash state changed the accepted prefix or retained input aliases")
			}
			var streaming hash.Hash64 = restored
			remaining := io.LimitReader(bytes.NewReader(contents[prefixLength:]), int64(len(contents)-prefixLength))
			if n, err := io.CopyBuffer(streaming, remaining, make([]byte, streaming.BlockSize()-1)); err != nil || n != int64(len(contents)-prefixLength) {
				t.Fatalf("native hash interface stream = %d bytes, %v", n, err)
			}
			if got, want := streaming.Sum64(), Sum64(contents); got != want {
				t.Fatalf("resumed streaming digest = %016x, want full payload %016x", got, want)
			}
			restored.Reset()
			catalogWriteDigest(t, restored, contents[:9])
			if restored.Sum64() != Sum64(contents[:9]) {
				t.Fatal("reset retained prior full-block accumulators or partial bytes")
			}
		})
	}
}

func TestCatalogDigestRejectsInvalidStateWithoutChangingCurrentHash(t *testing.T) {
	digest := New()
	catalogWriteDigest(t, digest, []byte("retained live payload"))
	state, err := digest.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	badIdentifier := bytes.Clone(state)
	badIdentifier[0] ^= 0x80
	for _, input := range [][]byte{nil, state[:1], badIdentifier, state[:len(state)-1], append(bytes.Clone(state), 0)} {
		before := digest.Sum64()
		if err := digest.UnmarshalBinary(input); err == nil || digest.Sum64() != before {
			t.Fatalf("invalid saved state changed current streaming hash: error=%v", err)
		}
	}
}
