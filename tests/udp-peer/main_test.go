package main

import (
	"net/netip"
	"testing"
)

func TestPeerRequiresExactSourceTupleAndPayload(t *testing.T) {
	expected := netip.MustParseAddrPort("192.0.2.6:27051")
	correct := []byte("mihomo-nonlocal-udp4-proof")
	for _, tc := range []struct {
		name, source string
		payload      []byte
		ok           bool
	}{
		{"correct", expected.String(), correct, true},
		{"wrong port", "192.0.2.6:27052", correct, false},
		{"wrong address", "192.0.2.5:27051", correct, false},
		{"altered payload", expected.String(), []byte("altered"), false},
		{"cross-family payload", expected.String(), []byte("mihomo-nonlocal-udp6-proof"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateReply(netip.MustParseAddrPort(tc.source), expected, tc.payload, "udp4")
			if (err == nil) != tc.ok {
				t.Fatal("peer accepted/rejected the wrong datagram")
			}
		})
	}
}
func TestMatrixChecksZeroLargeAndAlteredDatagrams(t *testing.T) {
	source := netip.MustParseAddrPort("192.0.2.6:27051")
	for _, size := range matrixSizes {
		data := matrixPayload("udp4", size)
		if validateMatrix(source, source, data, "udp4") != nil {
			t.Fatal("valid matrix rejected")
		}
		if size > 0 {
			data[0] ^= 1
			if validateMatrix(source, source, data, "udp4") == nil {
				t.Fatal("altered matrix accepted")
			}
		}
	}
	if validateMatrix(source, source, []byte{0, 1}, "udp4") == nil {
		t.Fatal("unexpected size accepted")
	}
}
