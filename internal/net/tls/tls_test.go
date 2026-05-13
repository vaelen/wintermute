// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package tls

import (
	"crypto/tls"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSelfSignedListen(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		Mode:     ModeSelfSigned,
		CertPath: filepath.Join(dir, "cert.pem"),
		KeyPath:  filepath.Join(dir, "key.pem"),
	}
	ln, err := Listen("127.0.0.1:0", cfg)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	// Spin up a tiny echo server.
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()

	// Dial with InsecureSkipVerify since the cert is self-signed.
	dialer := &tls.Dialer{
		Config: &tls.Config{InsecureSkipVerify: true},
	}
	conn, err := dialer.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	buf := make([]byte, 4)
	n, err := io.ReadFull(conn, buf)
	if err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	if string(buf[:n]) != "ping" {
		t.Errorf("got %q, want ping", buf[:n])
	}
}

func TestSelfSignedPersistsKeyPair(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		Mode:     ModeSelfSigned,
		CertPath: filepath.Join(dir, "c.pem"),
		KeyPath:  filepath.Join(dir, "k.pem"),
	}
	// First call generates files.
	if _, err := Listen("127.0.0.1:0", cfg); err != nil {
		t.Fatalf("Listen #1: %v", err)
	}
	// Second call should load the same files. We compare bytes to verify
	// the cert is reused rather than regenerated.
	cert1Bytes, err := os.ReadFile(cfg.CertPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Listen("127.0.0.1:0", cfg); err != nil {
		t.Fatalf("Listen #2: %v", err)
	}
	cert2Bytes, err := os.ReadFile(cfg.CertPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(cert1Bytes) != string(cert2Bytes) {
		t.Errorf("cert was regenerated; should have been reused")
	}
}

func TestFilesModeRequiresPaths(t *testing.T) {
	if _, err := Listen("127.0.0.1:0", Config{Mode: ModeFiles}); err == nil {
		t.Errorf("expected error when paths are missing")
	}
}

func TestAutocertRequiresHostnames(t *testing.T) {
	if _, err := Listen("127.0.0.1:0", Config{Mode: ModeAutocert}); err == nil {
		t.Errorf("expected error when hostnames are missing")
	}
}
