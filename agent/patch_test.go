package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func patchTestSHA(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func patchTestFiles(t *testing.T, fixture string) (string, string, string, []byte) {
	t.Helper()
	dir := t.TempDir()
	base, patch, out := filepath.Join(dir, "base"), filepath.Join(dir, "patch"), filepath.Join(dir, "out")
	data, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string][]byte{base: []byte("abcdef"), patch: data, out: []byte("existing core")} {
		if err := os.WriteFile(name, content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return base, patch, out, data
}

func patchTestPreserved(t *testing.T, out string) {
	t.Helper()
	got, err := os.ReadFile(out)
	if err != nil || string(got) != "existing core" {
		t.Fatalf("existing output changed: %q, %v", got, err)
	}
	entries, err := os.ReadDir(filepath.Dir(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("temporary file leaked: %v", entries)
	}
}

func TestApplyCorePatchMixedDataAndOldOffsets(t *testing.T) {
	base, patch, out, data := patchTestFiles(t, "patch-mixed.bsdiff")
	want := []byte("abXY!Zcd@AB?")
	if err := ApplyCorePatch(base, patch, out, patchTestSHA([]byte("abcdef")), patchTestSHA(data), patchTestSHA(want)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("got %q, %v", got, err)
	}
	info, err := os.Stat(out)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("output permissions: %v, %v", info, err)
	}
}

func TestApplyCorePatchEmptyOutput(t *testing.T) {
	base, patch, out, data := patchTestFiles(t, "patch-empty.bsdiff")
	if err := ApplyCorePatch(base, patch, out, patchTestSHA([]byte("abcdef")), patchTestSHA(data), patchTestSHA(nil)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestApplyCorePatchChunkedWithLargeSparseBase(t *testing.T) {
	base, patch, out, data := patchTestFiles(t, "patch-chunked.bsdiff")
	f, err := os.OpenFile(base, os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(56 * 1024 * 1024); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	// Deleting the destination also exercises the initial-boot restore path.
	if err := os.Remove(out); err != nil {
		t.Fatal(err)
	}
	want := make([]byte, 70000, 140000)
	for i := range want {
		want[i] = byte(i)
	}
	for i, b := range []byte("abcdef") {
		want[i] += b
	}
	want = append(want, bytes.Repeat([]byte("tail"), 17500)...)
	if err := ApplyCorePatch(base, patch, out, fileDigest(base), patchTestSHA(data), patchTestSHA(want)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("chunked output differs, length %d, error %v", len(got), err)
	}
}

func TestApplyCorePatchCrossesBothOldFileBounds(t *testing.T) {
	base, patch, out, data := patchTestFiles(t, "patch-old-eof.bsdiff")
	// 'a', then old offset 5: f, zero+A, zero+B; then old offset -1:
	// zero+C, a, b. This checks the overlapping portions of each chunk.
	want := []byte("afABCab")
	if err := ApplyCorePatch(base, patch, out, patchTestSHA([]byte("abcdef")), patchTestSHA(data), patchTestSHA(want)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestApplyCorePatchMinimumOldOffset(t *testing.T) {
	base, patch, out, data := patchTestFiles(t, "patch-minimum-old-offset.bsdiff")
	want := []byte{'a', 0, 0}
	if err := ApplyCorePatch(base, patch, out, patchTestSHA([]byte("abcdef")), patchTestSHA(data), patchTestSHA(want)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestApplyCorePatchSeekOnlyRecords(t *testing.T) {
	base, patch, out, data := patchTestFiles(t, "patch-seek-only.bsdiff")
	want := []byte("efbc")
	if err := ApplyCorePatch(base, patch, out, patchTestSHA([]byte("abcdef")), patchTestSHA(data), patchTestSHA(want)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestApplyCorePatchReleaseArtifact(t *testing.T) {
	base := filepath.Join("..", "work", "mihomo-armv7")
	patch := filepath.Join("..", "build", "release", "core-update.bsdiff")
	target := filepath.Join("..", "build", "udp-rootfix", "mihomo-linux-armv7-go1268")
	manifestPath := filepath.Join("..", "release", "core-release.json")
	for _, path := range []string{base, patch, target, manifestPath} {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Skip("optional local release artifacts unavailable")
		} else if err != nil {
			t.Fatal(err)
		}
	}
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		BaseSHA   string `json:"baseBinarySha256"`
		PatchSHA  string `json:"patchSha256"`
		TargetSHA string `json:"binarySha256"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	if fileDigest(target) != manifest.TargetSHA {
		t.Fatal("release target does not match manifest")
	}
	out := filepath.Join(t.TempDir(), "restored-core")
	if err := ApplyCorePatch(base, patch, out, manifest.BaseSHA, manifest.PatchSHA, manifest.TargetSHA); err != nil {
		t.Fatal(err)
	}
	if fileDigest(out) != manifest.TargetSHA {
		t.Fatal("restored release core does not match target")
	}
}

func TestApplyCorePatchHashFailuresPreserveOutput(t *testing.T) {
	for _, which := range []string{"base", "patch", "output", "malformed"} {
		t.Run(which, func(t *testing.T) {
			base, patch, out, data := patchTestFiles(t, "patch-mixed.bsdiff")
			baseSHA, patchSHA, outSHA := patchTestSHA([]byte("abcdef")), patchTestSHA(data), patchTestSHA([]byte("abXY!Zcd@AB?"))
			switch which {
			case "base":
				baseSHA = strings.Repeat("0", 64)
			case "patch":
				patchSHA = strings.Repeat("0", 64)
			case "output":
				outSHA = strings.Repeat("0", 64)
			case "malformed":
				baseSHA = ""
			}
			if err := ApplyCorePatch(base, patch, out, baseSHA, patchSHA, outSHA); err == nil {
				t.Fatal("accepted invalid hash")
			}
			patchTestPreserved(t, out)
		})
	}
}

func TestApplyCorePatchInvalidStreamsPreserveOutput(t *testing.T) {
	for _, test := range []struct{ fixture, reason string }{
		{"patch-negative-control.bsdiff", "invalid control record"},
		{"patch-negative-extra.bsdiff", "invalid control record"},
		{"patch-zero-progress.bsdiff", "invalid control record"},
		{"patch-overrun.bsdiff", "invalid control record"},
		{"patch-seek-overflow.bsdiff", "old seek overflow"},
		{"patch-seek-underflow.bsdiff", "old seek overflow"},
		{"patch-old-overflow.bsdiff", "old offset overflow"},
		{"patch-short-control.bsdiff", "invalid control stream"},
		{"patch-short-diff.bsdiff", "invalid diff stream"},
		{"patch-short-extra.bsdiff", "invalid extra stream"},
		{"patch-trailing-control.bsdiff", "trailing or corrupt stream"},
		{"patch-trailing-diff.bsdiff", "trailing or corrupt stream"},
		{"patch-trailing-extra.bsdiff", "trailing or corrupt stream"},
	} {
		t.Run(test.fixture, func(t *testing.T) {
			base, patch, out, data := patchTestFiles(t, test.fixture)
			err := ApplyCorePatch(base, patch, out, patchTestSHA([]byte("abcdef")), patchTestSHA(data), patchTestSHA([]byte("a")))
			if err == nil || !strings.Contains(err.Error(), test.reason) {
				t.Fatalf("wrong rejection for invalid stream: %v", err)
			}
			patchTestPreserved(t, out)
		})
	}
}

func TestApplyCorePatchRejectsExcessiveControlRecords(t *testing.T) {
	base, patch, out, data := patchTestFiles(t, "patch-record-limit.bsdiff")
	wouldProduce := make([]byte, 1048577)
	copy(wouldProduce, "abcdef")
	err := ApplyCorePatch(base, patch, out, patchTestSHA([]byte("abcdef")), patchTestSHA(data), patchTestSHA(wouldProduce))
	if err == nil || !strings.Contains(err.Error(), "too many control records") {
		t.Fatalf("wrong rejection for control expansion: %v", err)
	}
	patchTestPreserved(t, out)
}

func TestApplyCorePatchInvalidHeaderAndTruncation(t *testing.T) {
	for _, kind := range []string{"magic", "short-header", "negative-length", "length-overflow", "target-too-large", "truncated", "bad-bzip"} {
		t.Run(kind, func(t *testing.T) {
			base, patch, out, data := patchTestFiles(t, "patch-mixed.bsdiff")
			switch kind {
			case "magic":
				data[0] = 'X'
			case "short-header":
				data = data[:20]
			case "negative-length":
				data[15] |= 0x80
			case "length-overflow":
				for i := 8; i < 16; i++ {
					data[i] = 0xff
				}
				data[15] = 0x7f
			case "target-too-large":
				for i := 24; i < 32; i++ {
					data[i] = 0
				}
				data[27] = 6
			case "truncated":
				data = data[:len(data)-4]
			case "bad-bzip":
				data[32] = 'X'
			}
			if err := os.WriteFile(patch, data, 0600); err != nil {
				t.Fatal(err)
			}
			if err := ApplyCorePatch(base, patch, out, patchTestSHA([]byte("abcdef")), patchTestSHA(data), patchTestSHA([]byte("abXY!Zcd@AB?"))); err == nil {
				t.Fatal("accepted invalid patch")
			}
			patchTestPreserved(t, out)
		})
	}
}

func TestApplyCorePatchRejectsOversizedPatch(t *testing.T) {
	base, patch, out, _ := patchTestFiles(t, "patch-mixed.bsdiff")
	f, err := os.OpenFile(patch, os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(16*1024*1024 + 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ApplyCorePatch(base, patch, out, patchTestSHA([]byte("abcdef")), fileDigest(patch), patchTestSHA(nil)); err == nil {
		t.Fatal("accepted oversized patch")
	}
	patchTestPreserved(t, out)
}

func TestApplyCorePatchRejectsAliasedPaths(t *testing.T) {
	for _, kind := range []string{"base-output", "patch-output", "base-patch", "hardlink", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			base, patch, out, data := patchTestFiles(t, "patch-mixed.bsdiff")
			switch kind {
			case "base-output":
				out = base
			case "patch-output":
				out = patch
			case "base-patch":
				patch = base
			case "hardlink", "symlink":
				if err := os.Remove(out); err != nil {
					t.Fatal(err)
				}
				var err error
				if kind == "hardlink" {
					err = os.Link(base, out)
				} else {
					err = os.Symlink(base, out)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := ApplyCorePatch(base, patch, out, patchTestSHA([]byte("abcdef")), patchTestSHA(data), patchTestSHA(nil)); err == nil {
				t.Fatal("accepted aliased paths")
			}
			got, err := os.ReadFile(base)
			if err != nil || string(got) != "abcdef" {
				t.Fatalf("base changed: %q, %v", got, err)
			}
		})
	}
}
