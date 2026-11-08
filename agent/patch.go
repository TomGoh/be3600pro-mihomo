package main

import (
	"compress/bzip2"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
)

const (
	corePatchMaxBytes   = 16 * 1024 * 1024
	corePatchMaxOutput  = 80 * 1024 * 1024
	corePatchMaxRecords = 1 << 20
	corePatchChunkBytes = 32 * 1024
)

// ApplyCorePatch restores a core from a BSDIFF40 patch with bounded memory.
// All hashes are mandatory SHA-256 hex digests. The destination is replaced
// only after the input hashes, patch structure, and complete output are verified.
func ApplyCorePatch(basePath, patchPath, outputPath, baseSHA, patchSHA, outputSHA string) error {
	baseSHA, patchSHA, outputSHA = strings.ToLower(baseSHA), strings.ToLower(patchSHA), strings.ToLower(outputSHA)
	for _, digest := range []string{baseSHA, patchSHA, outputSHA} {
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != sha256.Size {
			return errors.New("core patch: invalid SHA-256")
		}
	}
	paths := []string{basePath, patchPath, outputPath}
	for i := range paths {
		absolute, err := filepath.Abs(paths[i])
		if err != nil {
			return errors.New("core patch: invalid path")
		}
		paths[i] = absolute
		for j := 0; j < i; j++ {
			if paths[i] == paths[j] {
				return errors.New("core patch: paths must be distinct")
			}
		}
	}
	base, err := os.Open(basePath)
	if err != nil {
		return errors.New("core patch: cannot open base")
	}
	defer base.Close()
	patch, err := os.Open(patchPath)
	if err != nil {
		return errors.New("core patch: cannot open patch")
	}
	defer patch.Close()
	baseInfo, err := base.Stat()
	if err != nil || !baseInfo.Mode().IsRegular() {
		return errors.New("core patch: invalid base file")
	}
	patchInfo, err := patch.Stat()
	if err != nil || !patchInfo.Mode().IsRegular() || patchInfo.Size() < 32 || patchInfo.Size() > corePatchMaxBytes {
		return errors.New("core patch: invalid patch file size")
	}
	if os.SameFile(baseInfo, patchInfo) {
		return errors.New("core patch: paths must be distinct")
	}
	outInfo, err := os.Stat(outputPath)
	if err != nil && !os.IsNotExist(err) {
		return errors.New("core patch: cannot inspect output")
	}
	if err == nil && (!outInfo.Mode().IsRegular() || os.SameFile(baseInfo, outInfo) || os.SameFile(patchInfo, outInfo)) {
		return errors.New("core patch: invalid or aliased output")
	}
	// Hash the same open descriptors used for decoding, so a path replacement
	// between verification and application cannot select a different input file.
	if !corePatchFileHash(base, baseInfo.Size(), baseSHA) {
		return errors.New("core patch: base checksum mismatch")
	}
	if !corePatchFileHash(patch, patchInfo.Size(), patchSHA) {
		return errors.New("core patch: patch checksum mismatch")
	}
	var header [32]byte
	if _, err := patch.ReadAt(header[:], 0); err != nil || string(header[:8]) != "BSDIFF40" {
		return errors.New("core patch: invalid header")
	}
	ctrlLen, diffLen, targetSize := corePatchInt(header[8:16]), corePatchInt(header[16:24]), corePatchInt(header[24:32])
	available := patchInfo.Size() - 32
	// Every bzip2 stream, including an empty one, needs at least 14 bytes.
	if ctrlLen < 14 || diffLen < 14 || ctrlLen > available || diffLen > available-ctrlLen || available-ctrlLen-diffLen < 14 || targetSize < 0 || targetSize > corePatchMaxOutput {
		return errors.New("core patch: invalid stream lengths")
	}
	ctrl := bzip2.NewReader(io.NewSectionReader(patch, 32, ctrlLen))
	diff := bzip2.NewReader(io.NewSectionReader(patch, 32+ctrlLen, diffLen))
	extra := bzip2.NewReader(io.NewSectionReader(patch, 32+ctrlLen+diffLen, available-ctrlLen-diffLen))
	output, err := os.CreateTemp(filepath.Dir(outputPath), ".core-patch-*")
	if err != nil {
		return errors.New("core patch: cannot create output")
	}
	defer os.Remove(output.Name())
	defer output.Close()
	if err := output.Chmod(0700); err != nil {
		return errors.New("core patch: cannot set output permissions")
	}
	digest := sha256.New()
	writer := io.MultiWriter(output, digest)
	var oldPos, newPos int64
	var control [24]byte
	var delta, old [corePatchChunkBytes]byte
	for records := 0; newPos < targetSize; records++ {
		if records >= corePatchMaxRecords {
			return errors.New("core patch: too many control records")
		}
		if _, err := io.ReadFull(ctrl, control[:]); err != nil {
			return errors.New("core patch: invalid control stream")
		}
		x, y, z := corePatchInt(control[:8]), corePatchInt(control[8:16]), corePatchInt(control[16:])
		remaining := targetSize - newPos
		// BSDIFF40 permits seek-only records: they reposition the base
		// without emitting output. Reject true no-ops; the record budget
		// also bounds arbitrarily long sequences of seek-only records.
		if x < 0 || y < 0 || x > remaining || y > remaining-x || x == 0 && y == 0 && z == 0 {
			return errors.New("core patch: invalid control record")
		}
		afterDiff, ok := corePatchAdd(oldPos, x)
		if !ok {
			return errors.New("core patch: old offset overflow")
		}
		nextOld, ok := corePatchAdd(afterDiff, z)
		if !ok {
			return errors.New("core patch: old seek overflow")
		}
		for left := x; left > 0; {
			n := min(left, int64(len(delta)))
			if _, err := io.ReadFull(diff, delta[:n]); err != nil {
				return errors.New("core patch: invalid diff stream")
			}
			// Only the intersection with the base contributes to the sum;
			// negative and beyond-EOF offsets act as zero, as in bspatch.
			start, end := max(oldPos, int64(0)), min(oldPos+n, baseInfo.Size())
			if start < end {
				count := end - start
				if _, err := base.ReadAt(old[:count], start); err != nil {
					return errors.New("core patch: cannot read base")
				}
				for i := int64(0); i < count; i++ {
					delta[start-oldPos+i] += old[i]
				}
			}
			if _, err := writer.Write(delta[:n]); err != nil {
				return errors.New("core patch: cannot write output")
			}
			oldPos += n // afterDiff was checked before this loop.
			left -= n
		}
		for left := y; left > 0; {
			n := min(left, int64(len(delta)))
			if _, err := io.ReadFull(extra, delta[:n]); err != nil {
				return errors.New("core patch: invalid extra stream")
			}
			if _, err := writer.Write(delta[:n]); err != nil {
				return errors.New("core patch: cannot write output")
			}
			left -= n
		}
		newPos += x + y
		oldPos = nextOld
	}
	// Reading to each stream's EOF validates bzip2 trailers/checksums even
	// when the last requested output byte ended before its trailer.
	for _, reader := range []io.Reader{ctrl, diff, extra} {
		var one [1]byte
		if n, err := reader.Read(one[:]); n != 0 || err != io.EOF {
			return errors.New("core patch: trailing or corrupt stream")
		}
	}
	if hex.EncodeToString(digest.Sum(nil)) != outputSHA {
		return errors.New("core patch: output checksum mismatch")
	}
	if err := output.Sync(); err != nil {
		return errors.New("core patch: cannot sync output")
	}
	if err := output.Close(); err != nil {
		return errors.New("core patch: cannot close output")
	}
	if err := os.Rename(output.Name(), outputPath); err != nil {
		return errors.New("core patch: cannot replace output")
	}
	return nil
}

func corePatchFileHash(file *os.File, size int64, expected string) bool {
	digest := sha256.New()
	n, err := io.Copy(digest, io.NewSectionReader(file, 0, size))
	return err == nil && n == size && hex.EncodeToString(digest.Sum(nil)) == expected
}

// BSDIFF40 integers use little-endian magnitude and a sign bit, not two's complement.
func corePatchInt(data []byte) int64 {
	value := int64(data[7] & 0x7f)
	for i := 6; i >= 0; i-- {
		value = value*256 + int64(data[i])
	}
	if data[7]&0x80 != 0 {
		return -value
	}
	return value
}

func corePatchAdd(a, b int64) (int64, bool) {
	if b > 0 && a > math.MaxInt64-b || b < 0 && a < math.MinInt64-b {
		return 0, false
	}
	return a + b, true
}
