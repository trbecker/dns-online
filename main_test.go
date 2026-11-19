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
failover1.local,failover2.local:/ha /mnt/ha nfs defaults 0 0
//smb1.internal.lan/share /mnt/smb1 cifs credentials=/etc/creds,_netdev 0 0
\\smb2.internal.lan\docs /mnt/smb2 smb3 defaults 0 0
//[2001:db8::20]/archive /mnt/smb-ipv6 cifs defaults 0 0
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

func TestParseInvalidFstab(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{
			name:    "too few fields",
			content: "nas1.internal.lan:/volume1/media /mnt/media nfs\n",
			wantErr: "too few fields",
		},
		{
			name:    "non-numeric dump frequency",
			content: "nas1.internal.lan:/volume1/media /mnt/media nfs defaults notanumber 0\n",
			wantErr: "is not a number",
		},
		{
			name:    "non-numeric fsck pass number",
			content: "nas1.internal.lan:/volume1/media /mnt/media nfs defaults 0 notanumber\n",
			wantErr: "not a number",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseFstab(strings.NewReader(tc.content), false)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("ParseFstab error = %q, want substring %q", err.Error(), tc.wantErr)
			}

			// Also verify ParseConfig propagates invalid fstab syntax errors when -fstab is enabled
			fstabFile := filepath.Join(t.TempDir(), "fstab")
			if writeErr := os.WriteFile(fstabFile, []byte(tc.content), 0600); writeErr != nil {
				t.Fatal(writeErr)
			}
			var stderr bytes.Buffer
			_, cfgErr := ParseConfig([]string{"-config=", "-fstab", "-fstab-path", fstabFile}, func(string) string { return "" }, &stderr)
			if cfgErr == nil {
				t.Fatalf("ParseConfig expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(cfgErr.Error(), tc.wantErr) {
				t.Errorf("ParseConfig error = %q, want substring %q", cfgErr.Error(), tc.wantErr)
			}
		})
	}
}

func TestParseConfigPrecedenceAndFstabMerge(t *testing.T) {
	tmpDir := t.TempDir()

	cfgFile := filepath.Join(tmpDir, "wait-dns-online.conf")
	err := os.WriteFile(cfgFile, []byte(`
# Sample config
WAIT_DNS_ONLINE_HOSTS="cfg-host1.local cfg-host2.local"
WAIT_DNS_ONLINE_FSTAB=true
WAIT_DNS_ONLINE_TIMEOUT=45s
WAIT_DNS_ONLINE_INTERVAL=500ms
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
		"WAIT_DNS_ONLINE_CONFIG":     cfgFile,
		"WAIT_DNS_ONLINE_FSTAB_PATH": fstabFile,
		"WAIT_DNS_ONLINE_TIMEOUT":    "30s", // Overrides config file's 45s
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
		cfg, err := ParseConfig([]string{"-config=", "-hosts", "flag1.local, flag2.local flag3.local"}, noopEnv, &stderr)
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
		cfg, err := ParseConfig([]string{"-config=", "pos1.local", "pos2.local,pos1.local", "pos3.local"}, noopEnv, &stderr)
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
		cfg, err := ParseConfig([]string{"-config=", "-hosts=flag1.local,flag2.local", "pos1.local", "flag1.local", "pos2.local"}, noopEnv, &stderr)
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

	cfgFile := filepath.Join(tmpDir, "wait-dns-online.conf")
	err := os.WriteFile(cfgFile, []byte(`
WAIT_DNS_ONLINE_HOSTS="cfg-host1.local cfg-host2.local"
WAIT_DNS_ONLINE_FSTAB=true
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
		"WAIT_DNS_ONLINE_CONFIG":     cfgFile,
		"WAIT_DNS_ONLINE_FSTAB_PATH": fstabFile,
		"WAIT_DNS_ONLINE_HOSTS":      "env-host1.local env-host2.local",
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
	cfgFile := filepath.Join(tmpDir, "wait-dns-online.conf")

	content := `
# Full-line hash comment
; Full-line semicolon comment

export WAIT_DNS_ONLINE_HOSTS="host1.example.com, host2.example.com # kept inside quotes"
WAIT_DNS_ONLINE_FSTAB = true # trailing inline comment
WAIT_DNS_ONLINE_FSTAB_PATH = '/custom/etc/fstab'

[wait-dns-online]
WAIT_DNS_ONLINE_TIMEOUT = 90s	# tab inline comment
WAIT_DNS_ONLINE_QUERY_TIMEOUT = 3s
`
	if err := os.WriteFile(cfgFile, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	got, err := parseEnvFile(cfgFile)
	if err != nil {
		t.Fatalf("parseEnvFile returned unexpected error: %v", err)
	}

	want := map[string]string{
		"WAIT_DNS_ONLINE_HOSTS":         "host1.example.com, host2.example.com # kept inside quotes",
		"WAIT_DNS_ONLINE_FSTAB":         "true",
		"WAIT_DNS_ONLINE_FSTAB_PATH":    "/custom/etc/fstab",
		"WAIT_DNS_ONLINE_TIMEOUT":       "90s",
		"WAIT_DNS_ONLINE_QUERY_TIMEOUT": "3s",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseEnvFile() = %#v, want %#v", got, want)
	}
}

func TestParseEnvFileMalformedLine(t *testing.T) {
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "bad.conf")

	if err := os.WriteFile(cfgFile, []byte("WAIT_DNS_ONLINE_FSTAB=true\nINVALID_LINE_WITHOUT_EQUALS\n"), 0600); err != nil {
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
WAIT_DNS_ONLINE_HOSTS="explicit.local"
WAIT_DNS_ONLINE_FSTAB=true
WAIT_DNS_ONLINE_FSTAB_PATH=` + fstabFile + `
WAIT_DNS_ONLINE_INCLUDE_NOAUTO=true
WAIT_DNS_ONLINE_ANY=true
WAIT_DNS_ONLINE_TIMEOUT=1m30s
WAIT_DNS_ONLINE_INTERVAL=2s
WAIT_DNS_ONLINE_QUERY_TIMEOUT=4s
WAIT_DNS_ONLINE_IPV4=true
WAIT_DNS_ONLINE_QUIET=true
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
		[]string{"-config", "systemd/wait-dns-online.default", "-fstab-path", fstabFile, "--"},
		func(string) string { return "" },
		&stderr,
	)
	if err != nil {
		t.Fatalf("ParseConfig with systemd/wait-dns-online.default failed: %v", err)
	}

	if !cfg.UseFstab {
		t.Errorf("UseFstab = false, want true from systemd/wait-dns-online.default")
	}
	if cfg.Timeout != 2*time.Minute {
		t.Errorf("Timeout = %v, want 2m from systemd/wait-dns-online.default", cfg.Timeout)
	}
	if cfg.Interval != 1*time.Second {
		t.Errorf("Interval = %v, want 1s from systemd/wait-dns-online.default", cfg.Interval)
	}
	if cfg.QueryTimeout != 5*time.Second {
		t.Errorf("QueryTimeout = %v, want 5s from systemd/wait-dns-online.default", cfg.QueryTimeout)
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
			name:       "invalid WAIT_DNS_ONLINE_HOSTS (whitespace/delimiters only)",
			cfgContent: "WAIT_DNS_ONLINE_HOSTS=\" , \t \"\nWAIT_DNS_ONLINE_FSTAB=false\n",
			wantErr:    "no hostnames specified",
		},
		{
			name:       "invalid WAIT_DNS_ONLINE_FSTAB",
			cfgContent: "WAIT_DNS_ONLINE_HOSTS=host.local\nWAIT_DNS_ONLINE_FSTAB=notabool\n",
			wantErr:    "invalid WAIT_DNS_ONLINE_FSTAB",
		},
		{
			name:       "invalid WAIT_DNS_ONLINE_FSTAB_PATH (nonexistent file when fstab enabled)",
			cfgContent: "WAIT_DNS_ONLINE_FSTAB=true\nWAIT_DNS_ONLINE_FSTAB_PATH=" + missingFstab + "\n",
			wantErr:    "opening fstab",
		},
		{
			name:       "invalid WAIT_DNS_ONLINE_INCLUDE_NOAUTO",
			cfgContent: "WAIT_DNS_ONLINE_HOSTS=host.local\nWAIT_DNS_ONLINE_INCLUDE_NOAUTO=notabool\n",
			wantErr:    "invalid WAIT_DNS_ONLINE_INCLUDE_NOAUTO",
		},
		{
			name:       "invalid WAIT_DNS_ONLINE_ANY",
			cfgContent: "WAIT_DNS_ONLINE_HOSTS=host.local\nWAIT_DNS_ONLINE_ANY=notabool\n",
			wantErr:    "invalid WAIT_DNS_ONLINE_ANY",
		},
		{
			name:       "invalid WAIT_DNS_ONLINE_TIMEOUT (unparseable duration)",
			cfgContent: "WAIT_DNS_ONLINE_HOSTS=host.local\nWAIT_DNS_ONLINE_TIMEOUT=notaduration\n",
			wantErr:    "invalid WAIT_DNS_ONLINE_TIMEOUT",
		},
		{
			name:       "invalid WAIT_DNS_ONLINE_TIMEOUT (negative duration)",
			cfgContent: "WAIT_DNS_ONLINE_HOSTS=host.local\nWAIT_DNS_ONLINE_TIMEOUT=-10s\n",
			wantErr:    "timeout cannot be negative",
		},
		{
			name:       "invalid WAIT_DNS_ONLINE_INTERVAL (unparseable duration)",
			cfgContent: "WAIT_DNS_ONLINE_HOSTS=host.local\nWAIT_DNS_ONLINE_INTERVAL=notaduration\n",
			wantErr:    "invalid WAIT_DNS_ONLINE_INTERVAL",
		},
		{
			name:       "invalid WAIT_DNS_ONLINE_INTERVAL (zero duration)",
			cfgContent: "WAIT_DNS_ONLINE_HOSTS=host.local\nWAIT_DNS_ONLINE_INTERVAL=0s\n",
			wantErr:    "interval must be greater than 0",
		},
		{
			name:       "invalid WAIT_DNS_ONLINE_QUERY_TIMEOUT (unparseable duration)",
			cfgContent: "WAIT_DNS_ONLINE_HOSTS=host.local\nWAIT_DNS_ONLINE_QUERY_TIMEOUT=notaduration\n",
			wantErr:    "invalid WAIT_DNS_ONLINE_QUERY_TIMEOUT",
		},
		{
			name:       "invalid WAIT_DNS_ONLINE_QUERY_TIMEOUT (zero duration)",
			cfgContent: "WAIT_DNS_ONLINE_HOSTS=host.local\nWAIT_DNS_ONLINE_QUERY_TIMEOUT=0s\n",
			wantErr:    "query-timeout must be greater than 0",
		},
		{
			name:       "invalid WAIT_DNS_ONLINE_IPV4",
			cfgContent: "WAIT_DNS_ONLINE_HOSTS=host.local\nWAIT_DNS_ONLINE_IPV4=notabool\n",
			wantErr:    "invalid WAIT_DNS_ONLINE_IPV4",
		},
		{
			name:       "invalid WAIT_DNS_ONLINE_IPV6",
			cfgContent: "WAIT_DNS_ONLINE_HOSTS=host.local\nWAIT_DNS_ONLINE_IPV6=notabool\n",
			wantErr:    "invalid WAIT_DNS_ONLINE_IPV6",
		},
		{
			name:       "mutually exclusive WAIT_DNS_ONLINE_IPV4 and WAIT_DNS_ONLINE_IPV6",
			cfgContent: "WAIT_DNS_ONLINE_HOSTS=host.local\nWAIT_DNS_ONLINE_IPV4=true\nWAIT_DNS_ONLINE_IPV6=true\n",
			wantErr:    "cannot specify both -4",
		},
		{
			name:       "invalid WAIT_DNS_ONLINE_QUIET",
			cfgContent: "WAIT_DNS_ONLINE_HOSTS=host.local\nWAIT_DNS_ONLINE_QUIET=notabool\n",
			wantErr:    "invalid WAIT_DNS_ONLINE_QUIET",
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
	if err := os.WriteFile(badBoolCfg, []byte("WAIT_DNS_ONLINE_FSTAB=notabool\n"), 0600); err != nil {
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
			args:    []string{"--config=" + missingCfg, "host.local"},
			wantErr: "loading config file",
		},
		{
			name:    "invalid boolean in config file",
			args:    []string{"-config=" + badBoolCfg, "host.local"},
			wantErr: "invalid WAIT_DNS_ONLINE_FSTAB",
		},
		{
			name:    "invalid duration in env",
			args:    []string{"-config=", "host.local"},
			env:     map[string]string{"WAIT_DNS_ONLINE_TIMEOUT": "notaduration"},
			wantErr: "invalid WAIT_DNS_ONLINE_TIMEOUT",
		},
		{
			name:    "both -4 and -6 specified",
			args:    []string{"-config=", "-4", "-6", "host.local"},
			wantErr: "cannot specify both -4",
		},
		{
			name:    "zero interval",
			args:    []string{"-config=", "-interval=0s", "host.local"},
			wantErr: "interval must be greater than 0",
		},
		{
			name:    "zero query timeout",
			args:    []string{"-config=", "-query-timeout=0s", "host.local"},
			wantErr: "query-timeout must be greater than 0",
		},
		{
			name:    "negative timeout",
			args:    []string{"-config=", "-timeout=-5s", "host.local"},
			wantErr: "timeout cannot be negative",
		},
		{
			name:    "no hosts and no fstab",
			args:    []string{"-config="},
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

func TestParseConfigInvalidCLIFlags(t *testing.T) {
	noopEnv := func(string) string { return "" }

	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "unknown flag",
			args:    []string{"-config=", "-unknown-flag", "host.local"},
			wantErr: "flag provided but not defined: -unknown-flag",
		},
		{
			name:    "invalid boolean flag value",
			args:    []string{"-config=", "-fstab=notabool", "host.local"},
			wantErr: "invalid boolean value \"notabool\" for -fstab",
		},
		{
			name:    "invalid timeout duration flag value",
			args:    []string{"-config=", "-timeout=notaduration", "host.local"},
			wantErr: "invalid value \"notaduration\" for flag -timeout",
		},
		{
			name:    "invalid interval duration flag value",
			args:    []string{"-config=", "-interval=notaduration", "host.local"},
			wantErr: "invalid value \"notaduration\" for flag -interval",
		},
		{
			name:    "invalid query-timeout duration flag value",
			args:    []string{"-config=", "-query-timeout=notaduration", "host.local"},
			wantErr: "invalid value \"notaduration\" for flag -query-timeout",
		},
		{
			name:    "missing argument for value flag",
			args:    []string{"-config=", "-hosts"},
			wantErr: "flag needs an argument: -hosts",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			_, err := ParseConfig(tc.args, noopEnv, &stderr)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("ParseConfig error = %q, want substring %q", err.Error(), tc.wantErr)
			}

			// Verify run() maps invalid CLI flags to exit code 2 (configuration error)
			var runStdout, runStderr bytes.Buffer
			code := run(context.Background(), tc.args, noopEnv, &mockResolver{}, &runStdout, &runStderr)
			if code != 2 {
				t.Errorf("run() exit code = %d, want 2", code)
			}
		})
	}
}

type mockResolver struct {
	mu       sync.Mutex
	calls    map[string]int
	networks []string
	handler  func(host string, attempt int) ([]net.IP, error)
}

func (m *mockResolver) LookupIP(ctx context.Context, network, host string) ([]net.IP, error) {
	m.mu.Lock()
	if m.calls == nil {
		m.calls = make(map[string]int)
	}
	m.calls[host]++
	attempt := m.calls[host]
	m.networks = append(m.networks, network)
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

func TestIPv6Only(t *testing.T) {
	var stderr bytes.Buffer
	cfg, err := ParseConfig([]string{"-config=", "-6", "ipv6-nas.local"}, func(string) string { return "" }, &stderr)
	if err != nil {
		t.Fatalf("ParseConfig(-6) returned unexpected error: %v", err)
	}
	if !cfg.IPv6Only || cfg.IPv4Only {
		t.Errorf("expected IPv6Only=true and IPv4Only=false, got IPv6Only=%v IPv4Only=%v", cfg.IPv6Only, cfg.IPv4Only)
	}
	if cfg.Network() != "ip6" {
		t.Errorf("cfg.Network() = %q, want %q", cfg.Network(), "ip6")
	}

	resolver := &mockResolver{
		handler: func(host string, attempt int) ([]net.IP, error) {
			return []net.IP{net.ParseIP("2001:db8::1")}, nil
		},
	}

	if err := WaitForDNS(context.Background(), cfg, resolver, func(string, ...any) {}); err != nil {
		t.Fatalf("WaitForDNS failed: %v", err)
	}

	if len(resolver.networks) != 1 || resolver.networks[0] != "ip6" {
		t.Errorf("LookupIP network calls = %v, want [\"ip6\"]", resolver.networks)
	}
}

func TestExtractInvalidRemoteSpecs(t *testing.T) {
	invalidNFS := []string{
		"",
		"   ",
		"no-colon-path",
		":/missing-host",
		"[2001:db8::1/missing-bracket",
		"[]:/empty-bracket",
		"user@nas.local:/export",
		"nfs://fileserver.example.com/pub",
	}
	for _, spec := range invalidNFS {
		if got := extractNFSHosts(spec); len(got) != 0 {
			t.Errorf("extractNFSHosts(%q) = %v, want empty", spec, got)
		}
	}

	invalidCIFS := []string{
		"",
		"   ",
		"not-a-unc-path",
		"//no-share",
		"//user@smb.local/share",
		"//smb.local:445/share",
		"//[2001:db8::20]:445/share",
		"//[2001:db8::20/missing-bracket",
		"//[]/empty-bracket",
		"smb://smb.local/share",
		"cifs://smb.local/share",
	}
	for _, spec := range invalidCIFS {
		if got := extractCIFSHosts(spec); len(got) != 0 {
			t.Errorf("extractCIFSHosts(%q) = %v, want empty", spec, got)
		}
	}
}

func TestRunExitCodes(t *testing.T) {
	noopEnv := func(string) string { return "" }

	t.Run("help exits 0", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"-config=", "-h"}, noopEnv, &mockResolver{}, &stdout, &stderr)
		if code != 0 {
			t.Errorf("run(-h) = %d, want 0", code)
		}
	})

	t.Run("resolved host exits 0 and logs unless quiet", func(t *testing.T) {
		resolver := &mockResolver{
			handler: func(host string, attempt int) ([]net.IP, error) {
				return []net.IP{net.ParseIP("10.0.0.1")}, nil
			},
		}

		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"-config=", "nas.local"}, noopEnv, resolver, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("run(nas.local) = %d, want 0 (stderr=%q)", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "Resolved nas.local") {
			t.Errorf("expected stdout log containing resolved host, got %q", stdout.String())
		}

		stdout.Reset()
		stderr.Reset()
		code = run(context.Background(), []string{"-config=", "-quiet", "nas.local"}, noopEnv, resolver, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("run(-quiet nas.local) = %d, want 0", code)
		}
		if stdout.Len() != 0 {
			t.Errorf("expected empty stdout with -quiet, got %q", stdout.String())
		}
	})

	t.Run("fstab with no remote mounts exits 0 immediately", func(t *testing.T) {
		fstabFile := filepath.Join(t.TempDir(), "fstab")
		if err := os.WriteFile(fstabFile, []byte("UUID=1234 / ext4 defaults 0 1\n"), 0600); err != nil {
			t.Fatal(err)
		}

		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"-config=", "-fstab", "-fstab-path", fstabFile}, noopEnv, &mockResolver{}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("run(-fstab empty) = %d, want 0 (stderr=%q)", code, stderr.String())
		}
	})

	t.Run("lookup timeout exits 1", func(t *testing.T) {
		resolver := &mockResolver{
			handler: func(host string, attempt int) ([]net.IP, error) {
				return nil, nil // empty IP slice with nil error
			},
		}

		var stdout, stderr bytes.Buffer
		code := run(
			context.Background(),
			[]string{"-config=", "-timeout=40ms", "-interval=10ms", "-query-timeout=10ms", "empty.local"},
			noopEnv,
			resolver,
			&stdout,
			&stderr,
		)
		if code != 1 {
			t.Errorf("run(timeout) = %d, want 1", code)
		}
		if !strings.Contains(stderr.String(), "no addresses returned") {
			t.Errorf("expected stderr to mention 'no addresses returned', got %q", stderr.String())
		}
	})
}
