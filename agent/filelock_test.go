package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestCoreInstallLockCannotBeStolenAndCancellationReleasesWaiter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core-install.lock")
	unlock, err := lockFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	second, err := lockFile(ctx, path)
	if err == nil {
		second()
		unlock()
		t.Fatal("parallel core installer stole lock")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	unlock()
	third, err := lockFile(context.Background(), path)
	if err != nil {
		t.Fatal("released lock stayed stale", err)
	}
	third()
}
