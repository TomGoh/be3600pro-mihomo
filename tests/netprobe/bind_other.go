//go:build !linux && !darwin

package main

import (
	"errors"
	"syscall"
)

func interfaceControl(name string) (func(string, string, syscall.RawConn) error, error) {
	if name != "" {
		return nil, errors.New("interface binding supported only on Linux and macOS")
	}
	return nil, nil
}
