package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

type CertificateInfo struct{ CertPath, KeyPath, Fingerprint, PublicKeyPin string }

func localHealthCurlConfig(identity CertificateInfo, token string) string {
	// --insecure skips CA/date validation only; curl still requires the exact
	// pinned public key. This file is only used with the fixed local LAN URL.
	return "silent\nshow-error\ninsecure\npinnedpubkey = \"" + identity.PublicKeyPin + "\"\nheader = \"Authorization: Bearer " + token + "\"\n"
}

func EnsureCertificate(root, host string) (CertificateInfo, error) {
	info := CertificateInfo{CertPath: filepath.Join(root, "server.crt"), KeyPath: filepath.Join(root, "server.key")}
	if net.ParseIP(host) == nil {
		return info, errors.New("TLS requires a literal LAN IP")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return info, err
	}
	_, certErr := os.Stat(info.CertPath)
	_, keyErr := os.Stat(info.KeyPath)
	if os.IsNotExist(certErr) && os.IsNotExist(keyErr) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return info, err
		}
		serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if err != nil {
			return info, err
		}
		now := time.Now()
		template := x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Router Proxy LAN"},
			NotBefore: now.Add(-24 * time.Hour), NotAfter: now.AddDate(10, 0, 0),
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			BasicConstraintsValid: true, IPAddresses: []net.IP{net.ParseIP(host)}}
		der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
		if err != nil {
			return info, err
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return info, err
		}
		if err = atomicWrite(info.KeyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})); err != nil {
			return info, err
		}
		if err = atomicWrite(info.CertPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})); err != nil {
			return info, err
		}
	}
	pair, err := tls.LoadX509KeyPair(info.CertPath, info.KeyPath)
	if err != nil {
		return info, errors.New("existing TLS identity is incomplete or invalid; preserve pairing and repair identity")
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return info, err
	}
	if err = cert.VerifyHostname(host); err != nil {
		return info, err
	}
	// The paired client validates certificate dates using its own clock. A router
	// without NTP must still serve its persisted identity for offline recovery.
	if err = os.Chmod(info.KeyPath, 0600); err != nil {
		return info, err
	}
	digest := sha256.Sum256(cert.Raw)
	info.Fingerprint = hex.EncodeToString(digest[:])
	publicKeyDigest := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	info.PublicKeyPin = "sha256//" + base64.StdEncoding.EncodeToString(publicKeyDigest[:])
	return info, nil
}
