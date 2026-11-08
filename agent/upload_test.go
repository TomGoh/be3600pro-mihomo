package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestValidatedCoreRecoveryAndReusedCoreRestoreDesiredProxy(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "replace", true: "reuse"}[reuse], func(t *testing.T) {
			s, _, service, root := serverFixture(t)
			service.enabled = false
			binary := make([]byte, 80)
			copy(binary, "\x7fELF")
			binary[4] = 1
			binary[5] = 1
			binary[18] = 40
			var compressed bytes.Buffer
			zw := gzip.NewWriter(&compressed)
			_, _ = zw.Write(binary)
			_ = zw.Close()
			archiveDigest := sha256.Sum256(compressed.Bytes())
			binaryDigest := sha256.Sum256(binary)
			s.options.ArchiveSHA256 = hex.EncodeToString(archiveDigest[:])
			s.options.BinarySHA256 = hex.EncodeToString(binaryDigest[:])
			s.options.ArchiveBytes = int64(compressed.Len())
			_ = os.WriteFile(filepath.Join(root, "desired-enabled"), []byte("1\n"), 0600)
			if reuse {
				_ = os.WriteFile(s.options.CorePath, binary, 0700)
			}
			request := httptest.NewRequest("POST", "/api/core", bytes.NewReader(compressed.Bytes()))
			request.Header.Set("Authorization", "Bearer "+fixtureToken)
			response := httptest.NewRecorder()
			s.ServeHTTP(response, request)
			if response.Code != 200 || service.starts != 1 || !service.enabled {
				t.Fatalf("recovery result status=%d starts=%d enabled=%v", response.Code, service.starts, service.enabled)
			}
		})
	}
}
