package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const FlashReserve = uint64(1024 * 1024)

var ErrRollback = errors.New("engine rollback failed")

type Subscription struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Path      string `json:"path"`
	UpdatedAt int64  `json:"updatedAt"`
	NodeCount int    `json:"nodeCount"`
}

type State struct {
	Subscriptions []Subscription    `json:"subscriptions"`
	Selections    map[string]string `json:"selections"`
}

type Store struct {
	Root      string
	FreeBytes func(string) (uint64, error)
}

type transaction struct {
	Config json.RawMessage `json:"config"`
	State  json.RawMessage `json:"state"`
}

func atomicWrite(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".router-write-")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOTSUP) {
		return err
	}
	return nil
}

func availableBytes(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}

func (s *Store) requireSpace(additional uint64) error {
	free := s.FreeBytes
	if free == nil {
		free = availableBytes
	}
	available, err := free(s.Root)
	if err != nil {
		return err
	}
	if available < FlashReserve+additional {
		return errors.New("闪存空间不足，已保留原配置")
	}
	return nil
}

func (s *Store) Snapshot() (map[string]any, State, error) {
	var config map[string]any
	var state State
	data, err := os.ReadFile(filepath.Join(s.Root, "config.json"))
	if err != nil {
		return nil, state, err
	}
	if err = json.Unmarshal(data, &config); err != nil {
		return nil, state, err
	}
	data, err = os.ReadFile(filepath.Join(s.Root, "state.json"))
	if err != nil && !os.IsNotExist(err) {
		return nil, state, err
	}
	if len(data) > 0 {
		if err = json.Unmarshal(data, &state); err != nil {
			return nil, state, err
		}
	}
	if state.Subscriptions == nil {
		state.Subscriptions = []Subscription{}
	}
	if state.Selections == nil {
		state.Selections = map[string]string{}
		if selected, e := os.ReadFile(filepath.Join(s.Root, "selections.json")); e == nil {
			_ = json.Unmarshal(selected, &state.Selections)
		}
	}
	return config, state, nil
}

func (s *Store) Commit(ctx context.Context, next map[string]any, state State,
	validate func(context.Context, string) error, reload func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if validate == nil || reload == nil {
		return errors.New("missing configuration validation/reload")
	}
	configPath := filepath.Join(s.Root, "config.json")
	statePath := filepath.Join(s.Root, "state.json")
	oldConfig, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	oldState, err := os.ReadFile(statePath)
	if os.IsNotExist(err) {
		oldState = []byte(`{"subscriptions":[],"selections":{}}`)
	} else if err != nil {
		return err
	}
	configData, err := json.Marshal(next)
	if err != nil {
		return err
	}
	stateData, err := json.Marshal(state)
	if err != nil {
		return err
	}
	journalData, err := json.Marshal(transaction{Config: oldConfig, State: oldState})
	if err != nil {
		return err
	}
	if err = s.requireSpace(uint64(len(configData) + len(stateData) + len(journalData) + len(oldConfig) + len(oldState))); err != nil {
		return err
	}
	candidate, err := os.CreateTemp(s.Root, ".candidate-*.json")
	if err != nil {
		return err
	}
	candidatePath := candidate.Name()
	defer os.Remove(candidatePath)
	if err = candidate.Chmod(0600); err == nil {
		_, err = candidate.Write(configData)
	}
	closeErr := candidate.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = validate(ctx, candidatePath); err != nil {
		return fmt.Errorf("配置校验失败，已保留原配置: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	journalPath := filepath.Join(s.Root, "transaction.json")
	if err = atomicWrite(journalPath, journalData); err != nil {
		return err
	}
	rollback := func(cause error) error {
		configErr := atomicWrite(configPath, oldConfig)
		stateErr := atomicWrite(statePath, oldState)
		recoveryCtx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
		defer cancel()
		reloadErr := reload(recoveryCtx)
		if configErr != nil || stateErr != nil || reloadErr != nil {
			return errors.Join(ErrRollback, cause, configErr, stateErr, reloadErr)
		}
		_ = os.Remove(journalPath)
		return fmt.Errorf("新配置未能加载，已恢复原配置: %w", cause)
	}
	if err = atomicWrite(configPath, configData); err != nil {
		return rollback(err)
	}
	if err = reload(ctx); err != nil {
		return rollback(err)
	}
	if err = atomicWrite(statePath, stateData); err != nil {
		return rollback(err)
	}
	if err = os.Remove(journalPath); err != nil {
		return rollback(err)
	}
	return nil
}

func RecoverStore(root string) error {
	path := filepath.Join(root, "transaction.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var journal transaction
	if err = json.Unmarshal(data, &journal); err != nil {
		return err
	}
	if !json.Valid(journal.Config) || !json.Valid(journal.State) {
		return errors.New("recovery journal is invalid")
	}
	if err = atomicWrite(filepath.Join(root, "config.json"), journal.Config); err != nil {
		return err
	}
	if err = atomicWrite(filepath.Join(root, "state.json"), journal.State); err != nil {
		return err
	}
	return os.Remove(path)
}
