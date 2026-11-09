package main

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func fileDigest(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	h := sha256.New()
	if _, err = io.Copy(h, file); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Server) uploadCore(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength >= 0 && r.ContentLength != s.options.ArchiveBytes {
		problem(w, 400, "内核包大小与固定版本不符")
		return
	}
	archive, err := os.CreateTemp(s.options.RunDir, ".core-upload-*.gz")
	if err != nil {
		problem(w, 500, "无法创建临时文件")
		return
	}
	defer os.Remove(archive.Name())
	_ = archive.Chmod(0600)
	digest := sha256.New()
	written, err := io.Copy(io.MultiWriter(archive, digest), http.MaxBytesReader(w, r.Body, s.options.ArchiveBytes+1))
	closeErr := archive.Close()
	if err != nil || closeErr != nil || written != s.options.ArchiveBytes || hex.EncodeToString(digest.Sum(nil)) != s.options.ArchiveSHA256 {
		problem(w, 400, "内核包校验失败，现有内核未改变")
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 100*time.Second)
	defer cancel()
	unlock, err := lockFile(ctx, filepath.Join(s.options.RunDir, "core-install.lock"))
	if err != nil {
		problem(w, 409, "核心正在恢复，请稍后重试")
		return
	}
	defer unlock()
	wasEnabled := s.options.Service.Enabled()
	desired, _ := os.ReadFile(filepath.Join(s.options.Root, "desired-enabled"))
	restore := wasEnabled || strings.TrimSpace(string(desired)) == "1"
	if s.options.BinarySHA256 != "" && fileDigest(s.options.CorePath) == s.options.BinarySHA256 {
		if err = s.options.Service.StartCore(ctx); err != nil {
			problem(w, 503, "内核文件已就绪，但核心未启动")
			return
		}
		_, state, _ := s.store.Snapshot()
		if err = s.restoreSelections(ctx, state); err != nil {
			problem(w, 503, "核心已启动，但节点选择尚未恢复")
			return
		}
		if restore {
			if err = s.setEnabled(ctx, true); err != nil {
				problem(w, 503, "核心已就绪，但代理启用失败")
				return
			}
		}
		_ = os.Remove(filepath.Join(s.options.RunDir, "bootstrap-error"))
		s.recordError("")
		reply(w, 200, map[string]bool{"ready": true, "reused": true})
		return
	}
	free, _ := availableBytes(s.options.RunDir)
	if free < 90*1024*1024 {
		problem(w, 507, "内存临时盘空间不足，现有服务未改变")
		return
	}
	input, err := os.Open(archive.Name())
	if err != nil {
		problem(w, 500, "无法读取内核包")
		return
	}
	defer input.Close()
	reader, err := gzip.NewReader(input)
	if err != nil {
		problem(w, 400, "内核压缩包无效")
		return
	}
	defer reader.Close()
	binaryFile, err := os.CreateTemp(filepath.Dir(s.options.CorePath), ".mihomo-next-")
	if err != nil {
		problem(w, 500, "无法准备内核")
		return
	}
	nextPath := binaryFile.Name()
	defer os.Remove(nextPath)
	n, err := io.Copy(binaryFile, io.LimitReader(reader, 80*1024*1024+1))
	closeErr = binaryFile.Close()
	if err != nil || closeErr != nil || n > 80*1024*1024 {
		problem(w, 400, "内核解压失败")
		return
	}
	if s.options.BinarySHA256 != "" && fileDigest(nextPath) != s.options.BinarySHA256 {
		problem(w, 400, "解压后内核校验失败")
		return
	}
	file, err := os.Open(nextPath)
	if err != nil {
		problem(w, 500, "无法检查内核")
		return
	}
	header := make([]byte, 20)
	_, err = io.ReadFull(file, header)
	file.Close()
	if err != nil || string(header[:4]) != "\x7fELF" || header[4] != 1 || header[5] != 1 || binary.LittleEndian.Uint16(header[18:]) != 40 {
		problem(w, 400, "此内核不适用于 ARMv7 路由器")
		return
	}
	if err = s.options.Service.StopCore(ctx); err != nil {
		problem(w, 503, "无法安全停止旧核心")
		return
	}
	previous := s.options.CorePath + ".previous"
	hadPrevious := false
	if _, err = os.Stat(s.options.CorePath); err == nil {
		if err = os.Rename(s.options.CorePath, previous); err != nil {
			_ = s.options.Service.StartCore(ctx)
			if wasEnabled {
				_ = s.setEnabled(ctx, true)
			}
			problem(w, 500, "无法备份旧内核")
			return
		}
		hadPrevious = true
	}
	if err = os.Chmod(nextPath, 0700); err == nil {
		err = os.Rename(nextPath, s.options.CorePath)
	}
	if err == nil {
		err = s.options.Service.StartCore(ctx)
	}
	if err != nil {
		if hadPrevious {
			_ = os.Rename(previous, s.options.CorePath)
			_ = s.options.Service.StartCore(ctx)
			if wasEnabled {
				_ = s.setEnabled(ctx, true)
			}
		}
		problem(w, 503, "内核恢复未完成，已尝试恢复旧服务")
		return
	}
	_ = os.Remove(previous)
	_, state, _ := s.store.Snapshot()
	s.restoreSelections(ctx, state)
	if restore {
		if err = s.setEnabled(ctx, true); err != nil {
			problem(w, 503, "内核已恢复，但节点尚未可用，代理保持关闭")
			return
		}
	}
	s.recordError("")
	_ = os.Remove(filepath.Join(s.options.RunDir, "bootstrap-error"))
	reply(w, 200, map[string]bool{"ready": true})
}
