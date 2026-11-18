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

func TestParseConfigCLIHosts(t *testing.T) {
	noopEnv := func(string) string { return "" }

	t.Run("flag hosts comma and space separated", func(t *testing.T) {
		var stderr bytes.Buffer
		cfg, err := ParseConfig([]string{"-hosts", "flag1.local, flag2.local flag3.local"}, noopEnv, &stderr)
		if err != nil {
			t.Fatalf("ParseConfig failed: %v", err)
		}
		want := []string{"flag1.local", "flag2.local", "flag3.local"}
		if !reflect.DeepEqual(cfg.Hosts, want) {
			t.Errorf("Hosts = %v, want %v", cfg.Hosts, want)
		}
	})

	t.Run("positional hosts with deduplication", func(t *testing.T) {
		var stderr bytes.Buffer
		cfg, err := ParseConfig([]string{"pos1.local", "pos2.local,pos1.local", "pos3.local"}, noopEnv, &stderr)
		if err != nil {
			t.Fatalf("ParseConfig failed: %v", err)
		}
		want := []string{"pos1.local", "pos2.local", "pos3.local"}
		if !reflect.DeepEqual(cfg.Hosts, want) {
			t.Errorf("Hosts = %v, want %v", cfg.Hosts, want)
		}
	})

	t.Run("flag hosts combined with positional hosts", func(t *testing.T) {
		var stderr bytes.Buffer
		cfg, err := ParseConfig([]string{"-hosts=flag1.local,flag2.local", "pos1.local", "flag1.local", "pos2.local"}, noopEnv, &stderr)
		if err != nil {
			t.Fatalf("ParseConfig failed: %v", err)
		}
		want := []string{"flag1.local", "flag2.local", "pos1.local", "pos2.local"}
		if !reflect.DeepEqual(cfg.Hosts, want) {
			t.Errorf("Hosts = %v, want %v", cfg.Hosts, want)
		}
	})
}

func TestParseConfigCLIHostsOverrideAndFstabMerge(t *testing.T) {
	tmpDir := t.TempDir()

	cfgFile := filepath.Join(tmpDir, "dns-online.conf")
	err := os.WriteFile(cfgFile, []byte(`
DNS_ONLINE_HOSTS="cfg-host1.local cfg-host2.local"
DNS_ONLINE_FSTAB=true
`), 0600)
	if err != nil {
		t.Fatal(err)
	}

	fstabFile := filepath.Join(tmpDir, "fstab")
	err = os.WriteFile(fstabFile, []byte(`
fstab-nas.local:/export /mnt/nfs nfs4 defaults 0 0
cli-pos.local:/share /mnt/share nfs defaults 0 0
//fstab-smb.local/docs /mnt/docs cifs defaults 0 0
`), 0600)
	if err != nil {
		t.Fatal(err)
	}

	envMap := map[string]string{
		"DNS_ONLINE_CONFIG":     cfgFile,
		"DNS_ONLINE_FSTAB_PATH": fstabFile,
		"DNS_ONLINE_HOSTS":      "env-host1.local env-host2.local",
	}
	getenv := func(k string) string { return envMap[k] }

	var stderr bytes.Buffer
	// Pass CLI hosts via both -hosts and positional args; they should override env/config hosts
	// and merge (with deduplication) with fstab hosts.
	cfg, err := ParseConfig(
		[]string{"-hosts", "cli-flag1.local,cli-flag2.local", "cli-pos.local"},
		getenv,
		&stderr,
	)
	if err != nil {
		t.Fatalf("ParseConfig failed: %v", err)
	}

	wantHosts := []string{
		"cli-flag1.local",
		"cli-flag2.local",
		"cli-pos.local",
		"fstab-nas.local",
		"fstab-smb.local",
	}
	if !reflect.DeepEqual(cfg.Hosts, wantHosts) {
		t.Errorf("Hosts = %v, want %v", cfg.Hosts, wantHosts)
	}
}

func TestParseEnvFileFormatsAndQuotes(t *testing.T) {
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "dns-online.conf")

	content := `
# Full-line hash comment
; Full-line semicolon comment

export DNS_ONLINE_HOSTS="host1.example.com, host2.example.com # kept inside quotes"
DNS_ONLINE_FSTAB = true # trailing inline comment
DNS_ONLINE_FSTAB_PATH = '/custom/etc/fstab'

[dns-online]
DNS_ONLINE_TIMEOUT = 90s	# tab inline comment
DNS_ONLINE_QUERY_TIMEOUT = 3s
`
	if err := os.WriteFile(cfgFile, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	got, err := parseEnvFile(cfgFile)
	if err != nil {
		t.Fatalf("parseEnvFile returned unexpected error: %v", err)
	}

	want := map[string]string{
		"DNS_ONLINE_HOSTS":         "host1.example.com, host2.example.com # kept inside quotes",
		"DNS_ONLINE_FSTAB":         "true",
		"DNS_ONLINE_FSTAB_PATH":    "/custom/etc/fstab",
		"DNS_ONLINE_TIMEOUT":       "90s",
		"DNS_ONLINE_QUERY_TIMEOUT": "3s",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseEnvFile() = %#v, want %#v", got, want)
	}
}

func TestParseEnvFileMalformedLine(t *testing.T) {
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "bad.conf")

	if err := os.WriteFile(cfgFile, []byte("DNS_ONLINE_FSTAB=true\nINVALID_LINE_WITHOUT_EQUALS\n"), 0600); err != nil {
		t.Fatal(err)
	}

	_, err := parseEnvFile(cfgFile)
	if err == nil {
		t.Fatal("expected error for malformed line without '=', got nil")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Errorf("expected error mentioning line 2, got: %v", err)
	}
}

func TestParseConfigAllOptionsFromConfigFile(t *testing.T) {
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "all-options.conf")
	fstabFile := filepath.Join(tmpDir, "fstab")

	if err := os.WriteFile(fstabFile, []byte("noauto-nas.local:/vol /mnt/vol nfs defaults,noauto 0 0\n"), 0600); err != nil {
		t.Fatal(err)
	}

	cfgContent := `
[DEFAULT]
DNS_ONLINE_HOSTS="explicit.local"
DNS_ONLINE_FSTAB=true
DNS_ONLINE_FSTAB_PATH=` + fstabFile + `
DNS_ONLINE_INCLUDE_NOAUTO=true
DNS_ONLINE_ANY=true
DNS_ONLINE_TIMEOUT=1m30s
DNS_ONLINE_INTERVAL=2s
DNS_ONLINE_QUERY_TIMEOUT=4s
DNS_ONLINE_IPV4=true
DNS_ONLINE_QUIET=true
`
	if err := os.WriteFile(cfgFile, []byte(cfgContent), 0600); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	cfg, err := ParseConfig([]string{"-config", cfgFile}, func(string) string { return "" }, &stderr)
	if err != nil {
		t.Fatalf("ParseConfig failed: %v", err)
	}

	if cfg.ConfigPath != cfgFile {
		t.Errorf("ConfigPath = %q, want %q", cfg.ConfigPath, cfgFile)
	}
	if !cfg.UseFstab || !cfg.IncludeNoauto || !cfg.WaitAny || !cfg.IPv4Only || cfg.IPv6Only || !cfg.Quiet {
		t.Errorf("unexpected boolean flags in parsed config: %+v", cfg)
	}
	if cfg.Network() != "ip4" {
		t.Errorf("Network() = %q, want ip4", cfg.Network())
	}
	if cfg.Timeout != 90*time.Second || cfg.Interval != 2*time.Second || cfg.QueryTimeout != 4*time.Second {
		t.Errorf("unexpected durations in parsed config: %+v", cfg)
	}
	wantHosts := []string{"explicit.local", "noauto-nas.local"}
	if !reflect.DeepEqual(cfg.Hosts, wantHosts) {
		t.Errorf("Hosts = %v, want %v", cfg.Hosts, wantHosts)
	}
}

func TestParseDefaultConfigFile(t *testing.T) {
	tmpDir := t.TempDir()
	fstabFile := filepath.Join(tmpDir, "fstab")
	if err := os.WriteFile(fstabFile, []byte("default-nas.local:/export /mnt/nfs nfs4 defaults 0 0\n"), 0600); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	cfg, err := ParseConfig(
		[]string{"-config", "systemd/dns-online.default", "-fstab-path", fstabFile},
		func(string) string { return "" },
		&stderr,
	)
	if err != nil {
		t.Fatalf("ParseConfig with systemd/dns-online.default failed: %v", err)
	}

	if !cfg.UseFstab {
		t.Errorf("UseFstab = false, want true from systemd/dns-online.default")
	}
	if cfg.Timeout != 2*time.Minute {
		t.Errorf("Timeout = %v, want 2m from systemd/dns-online.default", cfg.Timeout)
	}
	if cfg.Interval != 1*time.Second {
		t.Errorf("Interval = %v, want 1s from systemd/dns-online.default", cfg.Interval)
	}
	if cfg.QueryTimeout != 5*time.Second {
		t.Errorf("QueryTimeout = %v, want 5s from systemd/dns-online.default", cfg.QueryTimeout)
	}
	wantHosts := []string{"default-nas.local"}
	if !reflect.DeepEqual(cfg.Hosts, wantHosts) {
		t.Errorf("Hosts = %v, want %v", cfg.Hosts, wantHosts)
	}
}

func TestParseConfigInvalidConfigFileVariables(t *testing.T) {
	tmpDir := t.TempDir()
	missingFstab := filepath.Join(tmpDir, "nonexistent-fstab")

	tests := []struct {
		name       string
		cfgContent string
		wantErr    string
	}{
		{
			name:       "invalid DNS_ONLINE_HOSTS (whitespace/delimiters only)",
			cfgContent: "DNS_ONLINE_HOSTS=\" , \t \"\nDNS_ONLINE_FSTAB=false\n",
			wantErr:    "no hostnames specified",
		},
		{
			name:       "invalid DNS_ONLINE_FSTAB",
			cfgContent: "DNS_ONLINE_HOSTS=host.local\nDNS_ONLINE_FSTAB=notabool\n",
			wantErr:    "invalid DNS_ONLINE_FSTAB",
		},
		{
			name:       "invalid DNS_ONLINE_FSTAB_PATH (nonexistent file when fstab enabled)",
			cfgContent: "DNS_ONLINE_FSTAB=true\nDNS_ONLINE_FSTAB_PATH=" + missingFstab + "\n",
			wantErr:    "opening fstab",
		},
		{
			name:       "invalid DNS_ONLINE_INCLUDE_NOAUTO",
			cfgContent: "DNS_ONLINE_HOSTS=host.local\nDNS_ONLINE_INCLUDE_NOAUTO=notabool\n",
			wantErr:    "invalid DNS_ONLINE_INCLUDE_NOAUTO",
		},
		{
			name:       "invalid DNS_ONLINE_ANY",
			cfgContent: "DNS_ONLINE_HOSTS=host.local\nDNS_ONLINE_ANY=notabool\n",
			wantErr:    "invalid DNS_ONLINE_ANY",
		},
		{
			name:       "invalid DNS_ONLINE_TIMEOUT (unparseable duration)",
			cfgContent: "DNS_ONLINE_HOSTS=host.local\nDNS_ONLINE_TIMEOUT=notaduration\n",
			wantErr:    "invalid DNS_ONLINE_TIMEOUT",
		},
		{
			name:       "invalid DNS_ONLINE_TIMEOUT (negative duration)",
			cfgContent: "DNS_ONLINE_HOSTS=host.local\nDNS_ONLINE_TIMEOUT=-10s\n",
			wantErr:    "timeout cannot be negative",
		},
		{
			name:       "invalid DNS_ONLINE_INTERVAL (unparseable duration)",
			cfgContent: "DNS_ONLINE_HOSTS=host.local\nDNS_ONLINE_INTERVAL=notaduration\n",
			wantErr:    "invalid DNS_ONLINE_INTERVAL",
		},
		{
			name:       "invalid DNS_ONLINE_INTERVAL (zero duration)",
			cfgContent: "DNS_ONLINE_HOSTS=host.local\nDNS_ONLINE_INTERVAL=0s\n",
			wantErr:    "interval must be greater than 0",
		},
		{
			name:       "invalid DNS_ONLINE_QUERY_TIMEOUT (unparseable duration)",
			cfgContent: "DNS_ONLINE_HOSTS=host.local\nDNS_ONLINE_QUERY_TIMEOUT=notaduration\n",
			wantErr:    "invalid DNS_ONLINE_QUERY_TIMEOUT",
		},
		{
			name:       "invalid DNS_ONLINE_QUERY_TIMEOUT (zero duration)",
			cfgContent: "DNS_ONLINE_HOSTS=host.local\nDNS_ONLINE_QUERY_TIMEOUT=0s\n",
			wantErr:    "query-timeout must be greater than 0",
		},
		{
			name:       "invalid DNS_ONLINE_IPV4",
			cfgContent: "DNS_ONLINE_HOSTS=host.local\nDNS_ONLINE_IPV4=notabool\n",
			wantErr:    "invalid DNS_ONLINE_IPV4",
		},
		{
			name:       "invalid DNS_ONLINE_IPV6",
			cfgContent: "DNS_ONLINE_HOSTS=host.local\nDNS_ONLINE_IPV6=notabool\n",
			wantErr:    "invalid DNS_ONLINE_IPV6",
		},
		{
			name:       "mutually exclusive DNS_ONLINE_IPV4 and DNS_ONLINE_IPV6",
			cfgContent: "DNS_ONLINE_HOSTS=host.local\nDNS_ONLINE_IPV4=true\nDNS_ONLINE_IPV6=true\n",
			wantErr:    "cannot specify both -4",
		},
		{
			name:       "invalid DNS_ONLINE_QUIET",
			cfgContent: "DNS_ONLINE_HOSTS=host.local\nDNS_ONLINE_QUIET=notabool\n",
			wantErr:    "invalid DNS_ONLINE_QUIET",
		},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfgPath := filepath.Join(tmpDir, filepath.Base(t.Name())+".conf")
			if err := os.WriteFile(cfgPath, []byte(tc.cfgContent), 0600); err != nil {
				t.Fatalf("case %d: writing temp config: %v", i, err)
			}

			var stderr bytes.Buffer
			_, err := ParseConfig([]string{"-config", cfgPath}, func(string) string { return "" }, &stderr)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestParseConfigValidationErrors(t *testing.T) {
	tmpDir := t.TempDir()
	missingCfg := filepath.Join(tmpDir, "does-not-exist.conf")

	badBoolCfg := filepath.Join(tmpDir, "bad-bool.conf")
	if err := os.WriteFile(badBoolCfg, []byte("DNS_ONLINE_FSTAB=notabool\n"), 0600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		args    []string
		env     map[string]string
		wantErr string
	}{
		{
			name:    "explicit config file missing",
			args:    []string{"-config=" + missingCfg, "host.local"},
			wantErr: "loading config file",
		},
		{
			name:    "invalid boolean in config file",
			args:    []string{"-config=" + badBoolCfg, "host.local"},
			wantErr: "invalid DNS_ONLINE_FSTAB",
		},
		{
			name:    "invalid duration in env",
			args:    []string{"host.local"},
			env:     map[string]string{"DNS_ONLINE_TIMEOUT": "notaduration"},
			wantErr: "invalid DNS_ONLINE_TIMEOUT",
		},
		{
			name:    "both -4 and -6 specified",
			args:    []string{"-4", "-6", "host.local"},
			wantErr: "cannot specify both -4",
		},
		{
			name:    "zero interval",
			args:    []string{"-interval=0s", "host.local"},
			wantErr: "interval must be greater than 0",
		},
		{
			name:    "zero query timeout",
			args:    []string{"-query-timeout=0s", "host.local"},
			wantErr: "query-timeout must be greater than 0",
		},
		{
			name:    "negative timeout",
			args:    []string{"-timeout=-5s", "host.local"},
			wantErr: "timeout cannot be negative",
		},
		{
			name:    "no hosts and no fstab",
			args:    []string{},
			wantErr: "no hostnames specified",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			getenv := func(k string) string {
				if tc.env != nil {
					return tc.env[k]
				}
				return ""
			}
			var stderr bytes.Buffer
			_, err := ParseConfig(tc.args, getenv, &stderr)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.wantErr)
			}
		})
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
