package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseFstab(t *testing.T) {
	fstabContent := `
# /etc/fstab: static file system information.
UUID=1234-5678 / ext4 defaults 0 1
nas1.internal.lan:/volume1/media /mnt/media nfs defaults,_netdev 0 0
nas2.internal.lan:/export/data /mnt/data nfs4 rw,hard,intr 0 0 # inline comment
nas1.internal.lan:/volume1/other /mnt/other nfs defaults 0 0
backup.internal.lan:/backups /mnt/backups nfs rw,noauto 0 0
[2001:db8::10]:/share /mnt/ipv6 nfs4 defaults 0 0
nfs://fileserver.example.com:2049/pub /mnt/pub nfs4 defaults 0 0
failover1.local,failover2.local:/ha /mnt/ha nfs defaults 0 0
//smb1.internal.lan/share /mnt/smb1 cifs credentials=/etc/creds,_netdev 0 0
\\smb2.internal.lan\docs /mnt/smb2 smb3 defaults 0 0
//[2001:db8::20]:445/archive /mnt/smb-ipv6 cifs defaults 0 0
//smb-noauto.internal.lan/scratch /mnt/scratch cifs noauto,rw 0 0
`

	hosts, err := ParseFstab(strings.NewReader(fstabContent), false)
	if err != nil {
		t.Fatalf("ParseFstab returned unexpected error: %v", err)
	}

	expected := []string{
		"nas1.internal.lan",
		"nas2.internal.lan",
		"2001:db8::10",
		"fileserver.example.com",
		"failover1.local",
		"failover2.local",
		"smb1.internal.lan",
		"smb2.internal.lan",
		"2001:db8::20",
	}
	if !reflect.DeepEqual(hosts, expected) {
		t.Errorf("ParseFstab(includeNoauto=false) = %v, want %v", hosts, expected)
	}

	hostsWithNoauto, err := ParseFstab(strings.NewReader(fstabContent), true)
	if err != nil {
		t.Fatalf("ParseFstab(includeNoauto=true) error: %v", err)
	}
	expectedWithNoauto := []string{
		"nas1.internal.lan",
		"nas2.internal.lan",
		"backup.internal.lan",
		"2001:db8::10",
		"fileserver.example.com",
		"failover1.local",
		"failover2.local",
		"smb1.internal.lan",
		"smb2.internal.lan",
		"2001:db8::20",
		"smb-noauto.internal.lan",
	}
	if !reflect.DeepEqual(hostsWithNoauto, expectedWithNoauto) {
		t.Errorf("ParseFstab(includeNoauto=true) = %v, want %v", hostsWithNoauto, expectedWithNoauto)
	}
}

func TestParseConfigPrecedenceAndFstabMerge(t *testing.T) {
	tmpDir := t.TempDir()

	cfgFile := filepath.Join(tmpDir, "dns-online.conf")
	err := os.WriteFile(cfgFile, []byte(`
# Sample config
DNS_ONLINE_HOSTS="cfg-host1.local cfg-host2.local"
DNS_ONLINE_FSTAB=true
DNS_ONLINE_TIMEOUT=45s
DNS_ONLINE_INTERVAL=500ms
`), 0600)
	if err != nil {
		t.Fatal(err)
	}

	fstabFile := filepath.Join(tmpDir, "fstab")
	err = os.WriteFile(fstabFile, []byte(`
fstab-nas.local:/export /mnt/nfs nfs4 defaults 0 0
`), 0600)
	if err != nil {
		t.Fatal(err)
	}

	envMap := map[string]string{
		"DNS_ONLINE_CONFIG":     cfgFile,
		"DNS_ONLINE_FSTAB_PATH": fstabFile,
		"DNS_ONLINE_TIMEOUT":    "30s", // Overrides config file's 45s
	}
	getenv := func(k string) string { return envMap[k] }

	var stderr bytes.Buffer
	cfg, err := ParseConfig([]string{"-interval=250ms"}, getenv, &stderr)
	if err != nil {
		t.Fatalf("ParseConfig failed: %v", err)
	}

	if cfg.Timeout != 30*time.Second {
		t.Errorf("Timeout = %v, want 30s (from env)", cfg.Timeout)
	}
	if cfg.Interval != 250*time.Millisecond {
		t.Errorf("Interval = %v, want 250ms (from CLI flag)", cfg.Interval)
	}
	wantHosts := []string{"cfg-host1.local", "cfg-host2.local", "fstab-nas.local"}
	if !reflect.DeepEqual(cfg.Hosts, wantHosts) {
		t.Errorf("Hosts = %v, want %v", cfg.Hosts, wantHosts)
	}
}

type mockResolver struct {
	mu      sync.Mutex
	calls   map[string]int
	handler func(host string, attempt int) ([]net.IP, error)
}

func (m *mockResolver) LookupIP(ctx context.Context, network, host string) ([]net.IP, error) {
	m.mu.Lock()
	if m.calls == nil {
		m.calls = make(map[string]int)
	}
	m.calls[host]++
	attempt := m.calls[host]
	m.mu.Unlock()
	return m.handler(host, attempt)
}

func TestWaitForDNSAllHostsDelayed(t *testing.T) {
	resolver := &mockResolver{
		handler: func(host string, attempt int) ([]net.IP, error) {
			if host == "nas1.local" && attempt >= 2 {
				return []net.IP{net.ParseIP("10.0.0.10")}, nil
			}
			if host == "nas2.local" && attempt >= 3 {
				return []net.IP{net.ParseIP("10.0.0.20")}, nil
			}
			return nil, errors.New("temporary failure in name resolution")
		},
	}

	cfg := &Config{
		Hosts:        []string{"nas1.local", "nas2.local"},
		Timeout:      2 * time.Second,
		Interval:     20 * time.Millisecond,
		QueryTimeout: 100 * time.Millisecond,
	}

	var logs []string
	err := WaitForDNS(context.Background(), cfg, resolver, func(format string, args ...any) {
		logs = append(logs, format)
	})
	if err != nil {
		t.Fatalf("WaitForDNS returned unexpected error: %v", err)
	}

	if resolver.calls["nas1.local"] != 2 {
		t.Errorf("nas1.local calls = %d, want 2", resolver.calls["nas1.local"])
	}
	if resolver.calls["nas2.local"] != 3 {
		t.Errorf("nas2.local calls = %d, want 3", resolver.calls["nas2.local"])
	}
}

func TestWaitForDNSAnyHost(t *testing.T) {
	resolver := &mockResolver{
		handler: func(host string, attempt int) ([]net.IP, error) {
			if host == "nas2.local" {
				return []net.IP{net.ParseIP("10.0.0.20")}, nil
			}
			return nil, errors.New("no such host")
		},
	}

	cfg := &Config{
		Hosts:        []string{"nas1.local", "nas2.local"},
		WaitAny:      true,
		Timeout:      1 * time.Second,
		Interval:     20 * time.Millisecond,
		QueryTimeout: 100 * time.Millisecond,
	}

	err := WaitForDNS(context.Background(), cfg, resolver, func(string, ...any) {})
	if err != nil {
		t.Fatalf("WaitForDNS(WaitAny=true) failed: %v", err)
	}
}

func TestWaitForDNSTimeout(t *testing.T) {
	resolver := &mockResolver{
		handler: func(host string, attempt int) ([]net.IP, error) {
			return nil, errors.New("server misbehaving")
		},
	}

	cfg := &Config{
		Hosts:        []string{"unreachable.local"},
		Timeout:      60 * time.Millisecond,
		Interval:     20 * time.Millisecond,
		QueryTimeout: 20 * time.Millisecond,
	}

	err := WaitForDNS(context.Background(), cfg, resolver, func(string, ...any) {})
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "unreachable.local") {
		t.Errorf("error %q did not mention unresolved host", err.Error())
	}
}
