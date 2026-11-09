//go:build linux

package main

import (
	"net"
	"syscall"
)

func interfaceControl(name string) (func(string, string, syscall.RawConn) error, error) {
	if name == "" {
		return nil, nil
	}
	if _, e := net.InterfaceByName(name); e != nil {
		return nil, e
	}
	return func(network, address string, c syscall.RawConn) error {
		var sockErr error
		err := c.Control(func(fd uintptr) {
			sockErr = syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, name)
		})
		if err != nil {
			return err
		}
		return sockErr
	}, nil
}
