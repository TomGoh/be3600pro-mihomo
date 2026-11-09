//go:build darwin

package main

import (
	"net"
	"strings"
	"syscall"
)

func interfaceControl(name string) (func(string, string, syscall.RawConn) error, error) {
	if name == "" {
		return nil, nil
	}
	iface, e := net.InterfaceByName(name)
	if e != nil {
		return nil, e
	}
	return func(network, address string, c syscall.RawConn) error {
		var sockErr error
		err := c.Control(func(fd uintptr) {
			if strings.HasSuffix(network, "6") {
				sockErr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IPV6, syscall.IPV6_BOUND_IF, iface.Index)
			} else {
				sockErr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_BOUND_IF, iface.Index)
			}
		})
		if err != nil {
			return err
		}
		return sockErr
	}, nil
}
