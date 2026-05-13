// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package tls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

// Mode selects how server certificates are obtained.
type Mode int

const (
	// ModeSelfSigned generates (or loads from CertPath / KeyPath) a
	// long-lived self-signed cert. Suitable only for development.
	ModeSelfSigned Mode = iota
	// ModeFiles loads the cert chain and private key from CertPath and KeyPath.
	ModeFiles
	// ModeAutocert provisions certs via Let's Encrypt ACME. The HTTP-01
	// challenge listener runs on the OS-assigned address in HTTPListenAddr.
	ModeAutocert
)

// Config describes how to obtain TLS certificates.
type Config struct {
	Mode      Mode
	CertPath  string
	KeyPath   string
	CacheDir  string   // ModeAutocert only
	Hostnames []string // ModeAutocert only
}

// Listen returns a TLS listener bound to addr using the certificate
// strategy described by cfg.
func Listen(addr string, cfg Config) (net.Listener, error) {
	tlsCfg, err := buildTLSConfig(cfg)
	if err != nil {
		return nil, err
	}
	return tls.Listen("tcp", addr, tlsCfg)
}

func buildTLSConfig(cfg Config) (*tls.Config, error) {
	switch cfg.Mode {
	case ModeFiles:
		if cfg.CertPath == "" || cfg.KeyPath == "" {
			return nil, errors.New("tls: ModeFiles requires CertPath and KeyPath")
		}
		cert, err := tls.LoadX509KeyPair(cfg.CertPath, cfg.KeyPath)
		if err != nil {
			return nil, fmt.Errorf("tls: load cert: %w", err)
		}
		return &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}, nil

	case ModeAutocert:
		if len(cfg.Hostnames) == 0 {
			return nil, errors.New("tls: ModeAutocert requires at least one hostname")
		}
		m := &autocert.Manager{
			Cache:      autocert.DirCache(cfg.CacheDir),
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(cfg.Hostnames...),
		}
		return m.TLSConfig(), nil

	case ModeSelfSigned:
		cert, err := loadOrGenerateSelfSigned(cfg.CertPath, cfg.KeyPath)
		if err != nil {
			return nil, err
		}
		return &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}, nil

	default:
		return nil, fmt.Errorf("tls: unknown mode %d", cfg.Mode)
	}
}

// loadOrGenerateSelfSigned attempts to load a previously generated dev
// cert from certPath/keyPath; if either is empty or the files do not
// exist, a fresh cert + key is generated and persisted (if paths were
// given). The cert is valid for 10 years and contains "localhost" plus
// the loopback addresses as SANs.
func loadOrGenerateSelfSigned(certPath, keyPath string) (tls.Certificate, error) {
	if certPath != "" && keyPath != "" {
		if _, err := os.Stat(certPath); err == nil {
			if _, err2 := os.Stat(keyPath); err2 == nil {
				return tls.LoadX509KeyPair(certPath, keyPath)
			}
		}
	}
	cert, certPEM, keyPEM, err := generateSelfSigned()
	if err != nil {
		return tls.Certificate{}, err
	}
	if certPath != "" && keyPath != "" {
		if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
			return tls.Certificate{}, fmt.Errorf("tls: mkdir cert dir: %w", err)
		}
		if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
			return tls.Certificate{}, fmt.Errorf("tls: write cert: %w", err)
		}
		if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
			return tls.Certificate{}, fmt.Errorf("tls: write key: %w", err)
		}
	}
	return cert, nil
}

func generateSelfSigned() (tls.Certificate, []byte, []byte, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, nil, fmt.Errorf("tls: gen key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, nil, nil, err
	}
	now := time.Now()
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "wintermute-dev",
			Organization: []string{"Wintermute"},
		},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
		DNSNames:              []string{"localhost"},
		IPAddresses: []net.IP{
			net.IPv4(127, 0, 0, 1),
			net.IPv6loopback,
		},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, nil, nil, fmt.Errorf("tls: create cert: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return tls.Certificate{}, nil, nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, nil, nil, err
	}
	return cert, certPEM, keyPEM, nil
}
