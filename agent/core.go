package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type CoreAPI interface {
	Request(context.Context, string, string, any) (json.RawMessage, error)
}

type CoreClient struct {
	BaseURL, Token string
	Client         *http.Client
}

func (c *CoreClient) Request(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return nil, errors.New("invalid core endpoint")
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return nil, errors.New("内核请求格式错误")
	}
	request.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("内核请求失败或超时")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("内核拒绝请求（HTTP %d）", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 8*1024*1024+1))
	if err != nil || len(data) > 8*1024*1024 {
		return nil, errors.New("内核响应过大或不完整")
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if !json.Valid(data) {
		return nil, errors.New("内核响应格式错误")
	}
	return data, nil
}

type ScriptService struct{ Root, RunDir string }

func (s *ScriptService) Enabled() bool {
	data, _ := os.ReadFile(filepath.Join(s.RunDir, "enabled"))
	return strings.TrimSpace(string(data)) == "1"
}
func (s *ScriptService) run(ctx context.Context, action string) error {
	command := exec.CommandContext(ctx, "/bin/sh", filepath.Join(s.Root, "routerctl.sh"), action)
	command.Env = []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "CDX_EXPECT_EPOCH=") {
			command.Env = append(command.Env, entry)
		}
	}
	if intent, ok := ctx.Value(operationKey{}).(operationIntent); ok {
		command.Env = append(command.Env, "CDX_EXPECT_EPOCH="+intent.epoch)
	}
	if output, err := command.CombinedOutput(); err != nil {
		_ = os.WriteFile(filepath.Join(s.RunDir, "service-last-error.log"), output, 0600)
		return fmt.Errorf("路由器操作未完成：%s", action)
	}
	return nil
}
func (s *ScriptService) SetEnabled(ctx context.Context, on bool) error {
	action := "disable"
	if on {
		action = "enable"
	}
	return s.run(ctx, action)
}
func (s *ScriptService) StartCore(ctx context.Context) error { return s.run(ctx, "core-start") }
func (s *ScriptService) StopCore(ctx context.Context) error  { return s.run(ctx, "core-stop") }

type RouterService interface {
	Enabled() bool
	SetEnabled(context.Context, bool) error
	StartCore(context.Context) error
	StopCore(context.Context) error
}

type Options struct {
	Root, RunDir, CorePath, Token string
	Core                          CoreAPI
	Service                       RouterService
	Validate                      func(context.Context, string) error
	Download                      *http.Client
	ArchiveSHA256, BinarySHA256   string
	ArchiveBytes                  int64
	LeasePaths                    []string
	ARPPath                       string
}
