package main

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"strings"
	"testing"
)

func TestCertificateIsPinnedReusableAndPrivate(t *testing.T) {
	root := t.TempDir()
	first, err := EnsureCertificate(root, "192.168.31.1")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(first.CertPath)
	if err != nil {
		t.Fatal("server certificate was not created")
	}
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatal("certificate is not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err = cert.VerifyHostname("192.168.31.1"); err != nil {
		t.Fatal("certificate does not cover LAN address")
	}
	if len(first.Fingerprint) != 64 {
		t.Fatal("invalid SHA-256 pin")
	}
	info, err := os.Stat(first.KeyPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("TLS private key permissions are not 0600")
	}
	second, err := EnsureCertificate(root, "192.168.31.1")
	if err != nil {
		t.Fatal(err)
	}
	if second.Fingerprint != first.Fingerprint {
		t.Fatal("restart changed the paired certificate")
	}
}

func TestLocalHealthPinsPublicKeyWithoutDependingOnRouterClock(t *testing.T) {
	identity, err := EnsureCertificate(t.TempDir(), "192.168.31.1")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(identity.CertPath)
	block, _ := pem.Decode(data)
	cert, _ := x509.ParseCertificate(block.Bytes)
	digest := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	want := "sha256//" + base64.StdEncoding.EncodeToString(digest[:])
	config := localHealthCurlConfig(identity, "fixture-token")
	if identity.PublicKeyPin != want || !strings.Contains(config, "pinnedpubkey = \""+want+"\"") || !strings.Contains(config, "insecure\n") {
		t.Fatal("health check must authenticate the exact server public key without a router-clock date gate")
	}
	if strings.Contains(config, "cacert") || !strings.Contains(config, "Authorization: Bearer fixture-token") {
		t.Fatal("unexpected health authentication")
	}
}
