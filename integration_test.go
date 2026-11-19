//go:build integration

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var integrationBinary string

func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "wait-dns-online-integration-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmpDir)
	_ = os.Chmod(tmpDir, 0755)

	integrationBinary = filepath.Join(tmpDir, "wait-dns-online")
	cmd := exec.Command("go", "build", "-trimpath", "-tags", "netcgo", "-o", integrationBinary, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to build integration test binary: %v\n%s\n", err, string(out))
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// runBinary executes the compiled wait-dns-online binary with isolated environment variables.
func runBinary(t *testing.T, args []string, extraEnv map[string]string) (stdout, stderr string, exitCode int) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, integrationBinary, args...)
	env := []string{
		"PATH=" + os.Getenv("PATH"),
	}
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}
	cmd.Env = env

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("failed to execute %s %v: %v", integrationBinary, args, err)
		}
	}

	return outBuf.String(), errBuf.String(), exitCode
}

func TestIntegrationBinaryResolveLocalhost(t *testing.T) {
	t.Run("default dual-stack localhost", func(t *testing.T) {
		stdout, stderr, code := runBinary(t, []string{"-config=", "localhost"}, nil)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0 (stdout=%q stderr=%q)", code, stdout, stderr)
		}
		if !strings.Contains(stdout, "Resolved localhost ->") {
			t.Errorf("stdout = %q, want substring %q", stdout, "Resolved localhost ->")
		}
	})

	t.Run("ipv4-only localhost", func(t *testing.T) {
		stdout, stderr, code := runBinary(t, []string{"-config=", "-4", "localhost"}, nil)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0 (stdout=%q stderr=%q)", code, stdout, stderr)
		}
		if !strings.Contains(stdout, "127.0.0.1") {
			t.Errorf("stdout = %q, want IPv4 loopback 127.0.0.1", stdout)
		}
	})

	t.Run("ipv6-only localhost", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		ips, err := SystemResolver().LookupIP(ctx, "ip6", "localhost")
		if err != nil || len(ips) == 0 {
			t.Skipf("system resolver does not return IPv6 address for localhost: %v", err)
		}

		stdout, stderr, code := runBinary(t, []string{"-config=", "-6", "localhost"}, nil)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0 (stdout=%q stderr=%q)", code, stdout, stderr)
		}
		if !strings.Contains(stdout, "::1") {
			t.Errorf("stdout = %q, want IPv6 loopback ::1", stdout)
		}
	})

	t.Run("quiet suppresses stdout", func(t *testing.T) {
		stdout, stderr, code := runBinary(t, []string{"-config=", "-quiet", "localhost"}, nil)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
		}
		if stdout != "" {
			t.Errorf("stdout = %q, want empty string in -quiet mode", stdout)
		}
	})
}

func TestIntegrationBinaryConfigAndFstabEndToEnd(t *testing.T) {
	tmpDir := t.TempDir()

	fstabPath := filepath.Join(tmpDir, "fstab")
	fstabContent := strings.Join([]string{
		"UUID=1234-5678 / ext4 defaults 0 1",
		"localhost:/export/media /mnt/media nfs4 defaults,_netdev 0 0",
		"//localhost/share /mnt/smb cifs defaults,_netdev 0 0",
		"noauto.invalid:/backups /mnt/backups nfs rw,noauto 0 0",
	}, "\n") + "\n"
	if err := os.WriteFile(fstabPath, []byte(fstabContent), 0600); err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(tmpDir, "wait-dns-online.conf")
	cfgContent := strings.Join([]string{
		"# Integration test configuration",
		"WAIT_DNS_ONLINE_FSTAB=true",
		"WAIT_DNS_ONLINE_TIMEOUT=5s",
		"WAIT_DNS_ONLINE_INTERVAL=50ms",
		"WAIT_DNS_ONLINE_QUERY_TIMEOUT=1s",
	}, "\n") + "\n"
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0600); err != nil {
		t.Fatal(err)
	}

	env := map[string]string{
		"WAIT_DNS_ONLINE_CONFIG":     cfgPath,
		"WAIT_DNS_ONLINE_FSTAB_PATH": fstabPath,
	}

	t.Run("skips noauto entries and resolves active fstab hosts", func(t *testing.T) {
		stdout, stderr, code := runBinary(t, nil, env)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0 (stdout=%q stderr=%q)", code, stdout, stderr)
		}
		if !strings.Contains(stdout, "Resolved localhost ->") {
			t.Errorf("stdout = %q, want Resolved localhost", stdout)
		}
	})

	t.Run("include-noauto attempts to resolve noauto entry and times out", func(t *testing.T) {
		stdout, stderr, code := runBinary(
			t,
			[]string{"-include-noauto", "-timeout=200ms", "-interval=50ms", "-query-timeout=100ms"},
			env,
		)
		if code != 1 {
			t.Fatalf("exit code = %d, want 1 (stdout=%q stderr=%q)", code, stdout, stderr)
		}
		if !strings.Contains(stderr, "noauto.invalid") {
			t.Errorf("stderr = %q, want unresolved host noauto.invalid", stderr)
		}
	})
}

func TestIntegrationBinaryEmptyFstabGracefulBoot(t *testing.T) {
	fstabPath := filepath.Join(t.TempDir(), "fstab")
	content := "UUID=1234-5678 / ext4 defaults 0 1\nUUID=abcd-ef01 none swap sw 0 0\n"
	if err := os.WriteFile(fstabPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runBinary(t, []string{"-config=", "-fstab", "-fstab-path", fstabPath}, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stdout=%q stderr=%q)", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "No target hostnames to resolve") {
		t.Errorf("stdout = %q, want graceful empty-fstab message", stdout)
	}
}

func TestIntegrationBinaryTimeoutAndAnyMode(t *testing.T) {
	t.Run("unresolvable host times out with exit code 1", func(t *testing.T) {
		stdout, stderr, code := runBinary(
			t,
			[]string{"-config=", "-timeout=200ms", "-interval=50ms", "-query-timeout=100ms", "does-not-exist.invalid"},
			nil,
		)
		if code != 1 {
			t.Fatalf("exit code = %d, want 1 (stdout=%q stderr=%q)", code, stdout, stderr)
		}
		if !strings.Contains(stderr, "does-not-exist.invalid") {
			t.Errorf("stderr = %q, want unresolved host does-not-exist.invalid", stderr)
		}
	})

	t.Run("-any succeeds when one host resolves and another does not", func(t *testing.T) {
		stdout, stderr, code := runBinary(
			t,
			[]string{"-config=", "-any", "-timeout=2s", "-interval=50ms", "-query-timeout=200ms", "does-not-exist.invalid", "localhost"},
			nil,
		)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0 (stdout=%q stderr=%q)", code, stdout, stderr)
		}
		if !strings.Contains(stdout, "Resolved localhost ->") {
			t.Errorf("stdout = %q, want Resolved localhost", stdout)
		}
	})
}

func TestIntegrationBinaryConfigErrorsAndHelp(t *testing.T) {
	missingCfg := filepath.Join(t.TempDir(), "missing.conf")

	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStderr string
	}{
		{
			name:       "help flag exits 0",
			args:       []string{"-config=", "-h"},
			wantCode:   0,
			wantStderr: "Usage of wait-dns-online",
		},
		{
			name:       "mutually exclusive -4 and -6 exits 2",
			args:       []string{"-config=", "-4", "-6", "localhost"},
			wantCode:   2,
			wantStderr: "configuration error",
		},
		{
			name:       "unknown flag exits 2",
			args:       []string{"-config=", "-unknown-flag", "localhost"},
			wantCode:   2,
			wantStderr: "configuration error",
		},
		{
			name:       "missing explicit config file exits 2",
			args:       []string{"-config=" + missingCfg, "localhost"},
			wantCode:   2,
			wantStderr: "loading config file",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := runBinary(t, tc.args, nil)
			if code != tc.wantCode {
				t.Errorf("exit code = %d, want %d (stdout=%q stderr=%q)", code, tc.wantCode, stdout, stderr)
			}
			if !strings.Contains(stderr, tc.wantStderr) {
				t.Errorf("stderr = %q, want substring %q", stderr, tc.wantStderr)
			}
		})
	}
}

func TestIntegrationBinarySignalTermination(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(
		ctx,
		integrationBinary,
		"-config=",
		"-timeout=30s",
		"-interval=100ms",
		"-query-timeout=100ms",
		"sigterm-test.invalid",
	)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start binary: %v", err)
	}

	// Wait until wait-dns-online has registered its signal handler and entered WaitForDNS.
	ready := make(chan struct{})
	go func() {
		defer close(ready)
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), "Waiting for DNS resolution") {
				return
			}
		}
	}()

	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("timed out waiting for wait-dns-online startup log")
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("failed to send SIGTERM: %v", err)
	}

	waitErr := cmd.Wait()
	if waitErr == nil {
		t.Fatal("expected non-zero exit after SIGTERM, got exit code 0")
	}
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		t.Fatalf("expected *exec.ExitError, got %T: %v", waitErr, waitErr)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("exit code after SIGTERM = %d, want 1 (stderr=%q)", exitErr.ExitCode(), errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "context canceled") {
		t.Errorf("stderr = %q, want substring %q", errBuf.String(), "context canceled")
	}
}

// testDNSRecord holds A and AAAA addresses served by localTestDNSServer.
type testDNSRecord struct {
	ipv4 net.IP
	ipv6 net.IP
}

// localTestDNSServer is a minimal RFC 1035 UDP DNS responder for integration tests.
// It returns SERVFAIL for the first failCount queries per (host, qtype) before answering.
type localTestDNSServer struct {
	conn      net.PacketConn
	addr      string
	mu        sync.Mutex
	records   map[string]testDNSRecord
	failCount int
	attempts  map[string]int
}

func startLocalTestDNSServer(t *testing.T, records map[string]testDNSRecord, failCount int) *localTestDNSServer {
	t.Helper()

	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening on UDP: %v", err)
	}

	srv := &localTestDNSServer{
		conn:      conn,
		addr:      conn.LocalAddr().String(),
		records:   records,
		failCount: failCount,
		attempts:  make(map[string]int),
	}

	go srv.serve()
	t.Cleanup(func() { _ = conn.Close() })
	return srv
}

func (s *localTestDNSServer) serve() {
	buf := make([]byte, 512)
	for {
		n, peer, err := s.conn.ReadFrom(buf)
		if err != nil {
			return
		}
		if resp := s.buildResponse(buf[:n]); resp != nil {
			_, _ = s.conn.WriteTo(resp, peer)
		}
	}
}

func (s *localTestDNSServer) buildResponse(req []byte) []byte {
	if len(req) < 12 {
		return nil
	}

	qname, qEnd, ok := parseDNSQuestionName(req, 12)
	if !ok || qEnd+4 > len(req) {
		return nil
	}
	qtype := binary.BigEndian.Uint16(req[qEnd : qEnd+2])
	qclass := binary.BigEndian.Uint16(req[qEnd+2 : qEnd+4])
	questionSection := req[12 : qEnd+4]

	host := strings.ToLower(strings.TrimSuffix(qname, "."))

	s.mu.Lock()
	rec, found := s.records[host]
	attemptKey := fmt.Sprintf("%s:%d", host, qtype)
	var attempt int
	if found {
		s.attempts[attemptKey]++
		attempt = s.attempts[attemptKey]
	}
	failCount := s.failCount
	s.mu.Unlock()

	var rcode uint16
	var answer []byte

	switch {
	case !found || qclass != 1:
		rcode = 3 // NXDOMAIN
	case attempt <= failCount:
		rcode = 2 // SERVFAIL (simulate upstream DNS not ready yet)
	case qtype == 1 && rec.ipv4 != nil: // A
		ip4 := rec.ipv4.To4()
		answer = encodeDNSAnswer(1, ip4)
	case qtype == 28 && rec.ipv6 != nil: // AAAA
		ip6 := rec.ipv6.To16()
		answer = encodeDNSAnswer(28, ip6)
	default:
		rcode = 0 // NOERROR, empty answer (NODATA)
	}

	resp := make([]byte, 12, 12+len(questionSection)+len(answer))
	copy(resp[0:2], req[0:2]) // Transaction ID
	// Flags: QR=1, AA=1, RD=copied, RA=1, RCODE
	resp[2] = 0x84 | (req[2] & 0x01)
	resp[3] = 0x80 | byte(rcode&0x0F)
	binary.BigEndian.PutUint16(resp[4:6], 1) // QDCOUNT
	if len(answer) > 0 {
		binary.BigEndian.PutUint16(resp[6:8], 1) // ANCOUNT
	}
	resp = append(resp, questionSection...)
	resp = append(resp, answer...)
	return resp
}

func parseDNSQuestionName(pkt []byte, offset int) (string, int, bool) {
	var labels []string
	i := offset
	for i < len(pkt) {
		l := int(pkt[i])
		i++
		if l == 0 {
			return strings.Join(labels, "."), i, true
		}
		if i+l > len(pkt) {
			return "", 0, false
		}
		labels = append(labels, string(pkt[i:i+l]))
		i += l
	}
	return "", 0, false
}

func encodeDNSAnswer(qtype uint16, rdata []byte) []byte {
	ans := make([]byte, 12+len(rdata))
	ans[0] = 0xC0 // Name pointer to offset 12
	ans[1] = 0x0C
	binary.BigEndian.PutUint16(ans[2:4], qtype)
	binary.BigEndian.PutUint16(ans[4:6], 1)  // Class IN
	binary.BigEndian.PutUint32(ans[6:10], 60) // TTL 60s
	binary.BigEndian.PutUint16(ans[10:12], uint16(len(rdata)))
	copy(ans[12:], rdata)
	return ans
}

func TestIntegrationLiveUDPDNSServer(t *testing.T) {
	srv := startLocalTestDNSServer(t, map[string]testDNSRecord{
		"nfs1.test.local": {
			ipv4: net.ParseIP("192.0.2.10"),
			ipv6: net.ParseIP("2001:db8::10"),
		},
		"smb1.test.local": {
			ipv4: net.ParseIP("192.0.2.20"),
			ipv6: net.ParseIP("2001:db8::20"),
		},
	}, 2) // Return SERVFAIL for the first 2 attempts per (host, qtype)

	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "udp", srv.addr)
		},
	}

	fstabPath := filepath.Join(t.TempDir(), "fstab")
	fstabContent := "nfs1.test.local:/export /mnt/nfs nfs4 defaults 0 0\n//smb1.test.local/share /mnt/smb cifs defaults 0 0\n"
	if err := os.WriteFile(fstabPath, []byte(fstabContent), 0600); err != nil {
		t.Fatal(err)
	}

	t.Run("recovers after transient SERVFAIL for fstab hosts (IPv4)", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run(
			context.Background(),
			[]string{"-config=", "-4", "-fstab", "-fstab-path", fstabPath, "-timeout=5s", "-interval=25ms", "-query-timeout=500ms"},
			func(string) string { return "" },
			resolver,
			&stdout,
			&stderr,
		)
		if code != 0 {
			t.Fatalf("run() = %d, want 0 (stdout=%q stderr=%q)", code, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "Resolved nfs1.test.local -> 192.0.2.10") {
			t.Errorf("stdout = %q, want nfs1.test.local -> 192.0.2.10", stdout.String())
		}
		if !strings.Contains(stdout.String(), "Resolved smb1.test.local -> 192.0.2.20") {
			t.Errorf("stdout = %q, want smb1.test.local -> 192.0.2.20", stdout.String())
		}
	})

	t.Run("recovers after transient SERVFAIL for IPv6-only lookup", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run(
			context.Background(),
			[]string{"-config=", "-6", "-timeout=5s", "-interval=25ms", "-query-timeout=500ms", "nfs1.test.local"},
			func(string) string { return "" },
			resolver,
			&stdout,
			&stderr,
		)
		if code != 0 {
			t.Fatalf("run() = %d, want 0 (stdout=%q stderr=%q)", code, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "Resolved nfs1.test.local -> 2001:db8::10") {
			t.Errorf("stdout = %q, want nfs1.test.local -> 2001:db8::10", stdout.String())
		}
	})
}

func TestIntegrationSystemdUnitFile(t *testing.T) {
	raw, err := os.ReadFile("systemd/wait-dns-online.service")
	if err != nil {
		t.Fatalf("reading systemd/wait-dns-online.service: %v", err)
	}
	unit := string(raw)

	for _, required := range []string{
		"Before=remote-fs-pre.target",
		"network-online.target",
		"nss-lookup.target",
		"Type=oneshot",
	} {
		if !strings.Contains(unit, required) {
			t.Errorf("systemd/wait-dns-online.service missing %q", required)
		}
	}

	if _, err := exec.LookPath("systemd-analyze"); err != nil {
		t.Skip("systemd-analyze not installed; skipping unit verification")
	}

	tmpUnit := filepath.Join(t.TempDir(), "wait-dns-online.service")
	patched := strings.ReplaceAll(unit, "/usr/bin/wait-dns-online", integrationBinary)
	if err := os.WriteFile(tmpUnit, []byte(patched), 0644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("systemd-analyze", "verify", tmpUnit)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("systemd-analyze verify failed: %v\n%s", err, string(out))
	}
}

type systemdUnitDump struct {
	fragmentPath string
	wants        map[string]bool
	before       map[string]bool
	after        map[string]bool
	startQueued  bool
}

// parseSystemdTestDump parses the unit sections from `systemd --test --system` output.
func parseSystemdTestDump(output string) map[string]*systemdUnitDump {
	units := make(map[string]*systemdUnitDump)
	var current *systemdUnitDump

	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "\t→ Unit ") && strings.HasSuffix(line, ":") {
			name := strings.TrimSuffix(strings.TrimPrefix(line, "\t→ Unit "), ":")
			current = &systemdUnitDump{
				wants:  make(map[string]bool),
				before: make(map[string]bool),
				after:  make(map[string]bool),
			}
			units[name] = current
			continue
		}
		if current == nil {
			continue
		}
		if !strings.HasPrefix(line, "\t\t") {
			current = nil
			continue
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "Fragment Path: "):
			current.fragmentPath = strings.TrimPrefix(trimmed, "Fragment Path: ")
		case strings.HasPrefix(trimmed, "Wants: "):
			target := strings.Fields(strings.TrimPrefix(trimmed, "Wants: "))[0]
			current.wants[target] = true
		case strings.HasPrefix(trimmed, "Before: "):
			target := strings.Fields(strings.TrimPrefix(trimmed, "Before: "))[0]
			current.before[target] = true
		case strings.HasPrefix(trimmed, "After: "):
			target := strings.Fields(strings.TrimPrefix(trimmed, "After: "))[0]
			current.after[target] = true
		case strings.HasPrefix(trimmed, "Action: ") && strings.HasSuffix(trimmed, " -> start"):
			current.startQueued = true
		}
	}
	return units
}

func findFirstExecutable(paths ...string) string {
	for _, p := range paths {
		if info, err := os.Stat(p); err == nil && !info.IsDir() && info.Mode()&0111 != 0 {
			return p
		}
	}
	return ""
}

func TestIntegrationSystemdNFSOrdering(t *testing.T) {
	systemdBin := findFirstExecutable("/usr/lib/systemd/systemd", "/lib/systemd/systemd")
	if systemdBin == "" {
		t.Skip("systemd binary not found; skipping systemd ordering integration test")
	}

	tmpDir, err := os.MkdirTemp("", "wait-dns-online-systemd-ordering-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })
	// Ensure tmpDir is traversable when dropping privileges from root (e.g. inside containers).
	if err := os.Chmod(tmpDir, 0755); err != nil {
		t.Fatal(err)
	}
	unitsDir := filepath.Join(tmpDir, "units")
	if err := os.MkdirAll(filepath.Join(unitsDir, "remote-fs.target.wants"), 0755); err != nil {
		t.Fatal(err)
	}

	// Place repository's wait-dns-online.service (pointing ExecStart to compiled test binary)
	// and enable it under remote-fs.target.wants, matching [Install] WantedBy=remote-fs.target.
	rawUnit, err := os.ReadFile("systemd/wait-dns-online.service")
	if err != nil {
		t.Fatalf("reading systemd/wait-dns-online.service: %v", err)
	}
	dnsOnlineUnitPath := filepath.Join(unitsDir, "wait-dns-online.service")
	patchedUnit := strings.ReplaceAll(string(rawUnit), "/usr/bin/wait-dns-online", integrationBinary)
	if err := os.WriteFile(dnsOnlineUnitPath, []byte(patchedUnit), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../wait-dns-online.service", filepath.Join(unitsDir, "remote-fs.target.wants", "wait-dns-online.service")); err != nil {
		t.Fatal(err)
	}

	// Generate an NFS .mount unit using systemd-fstab-generator when available,
	// matching real Linux boot behavior for /etc/fstab NFS entries.
	const nfsMountUnit = "mnt-testnfs.mount"
	fstabGen := findFirstExecutable(
		"/usr/lib/systemd/system-generators/systemd-fstab-generator",
		"/lib/systemd/system-generators/systemd-fstab-generator",
	)
	if fstabGen != "" {
		fstabPath := filepath.Join(tmpDir, "fstab")
		if err := os.WriteFile(fstabPath, []byte("nas.test.local:/export /mnt/testnfs nfs4 defaults 0 0\n"), 0644); err != nil {
			t.Fatal(err)
		}
		earlyDir := filepath.Join(tmpDir, "early")
		lateDir := filepath.Join(tmpDir, "late")
		if err := os.MkdirAll(earlyDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(lateDir, 0755); err != nil {
			t.Fatal(err)
		}

		genCmd := exec.Command(fstabGen, unitsDir, earlyDir, lateDir)
		genCmd.Env = append(os.Environ(),
			"SYSTEMD_FSTAB="+fstabPath,
			"SYSTEMD_IN_INITRD=0",
		)
		if out, err := genCmd.CombinedOutput(); err != nil {
			t.Fatalf("systemd-fstab-generator failed: %v\n%s", err, string(out))
		}
	} else {
		if err := os.MkdirAll(filepath.Join(unitsDir, "remote-fs.target.requires"), 0755); err != nil {
			t.Fatal(err)
		}
		mountContent := "[Unit]\nBefore=remote-fs.target\n\n[Mount]\nWhat=nas.test.local:/export\nWhere=/mnt/testnfs\nType=nfs4\n"
		if err := os.WriteFile(filepath.Join(unitsDir, nfsMountUnit), []byte(mountContent), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../"+nfsMountUnit, filepath.Join(unitsDir, "remote-fs.target.requires", nfsMountUnit)); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Trailing colon prepends unitsDir ahead of the system's built-in unit search path.
	cmd := exec.CommandContext(ctx, systemdBin, "--test", "--system", "--unit=remote-fs.target")
	cmd.Env = append(os.Environ(), "SYSTEMD_UNIT_PATH="+unitsDir+":")
	// systemd --test refuses to run as UID 0 ("Don't run test mode as root.").
	// Drop privileges to nobody when tests run as root (such as inside build containers).
	if os.Geteuid() == 0 {
		uid, gid := uint32(65534), uint32(65534)
		if u, lookupErr := user.Lookup("nobody"); lookupErr == nil {
			if parsedUID, err := strconv.ParseUint(u.Uid, 10, 32); err == nil {
				uid = uint32(parsedUID)
			}
			if parsedGID, err := strconv.ParseUint(u.Gid, 10, 32); err == nil {
				gid = uint32(parsedGID)
			}
		}
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Credential: &syscall.Credential{
				Uid: uid,
				Gid: gid,
			},
		}
	}
	outBytes, err := cmd.CombinedOutput()
	output := string(outBytes)
	if err != nil {
		t.Fatalf("systemd --test failed: %v\n%s", err, output)
	}

	// Ensure systemd did not detect any ordering cycle involving our units.
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "Ordering cycle") || strings.Contains(line, "Breaking ordering cycle") {
			for _, u := range []string{"wait-dns-online.service", "remote-fs-pre.target", nfsMountUnit, "remote-fs.target"} {
				if strings.Contains(line, u) {
					t.Errorf("systemd reported an ordering cycle involving %s: %s", u, line)
				}
			}
		}
	}

	units := parseSystemdTestDump(output)

	dnsOnline := units["wait-dns-online.service"]
	remoteFsPre := units["remote-fs-pre.target"]
	nfsMount := units[nfsMountUnit]
	remoteFs := units["remote-fs.target"]

	for name, u := range map[string]*systemdUnitDump{
		"wait-dns-online.service":   dnsOnline,
		"remote-fs-pre.target": remoteFsPre,
		nfsMountUnit:           nfsMount,
		"remote-fs.target":     remoteFs,
	} {
		if u == nil {
			t.Fatalf("unit %s missing from systemd --test dump", name)
		}
		if !u.startQueued {
			t.Errorf("unit %s does not have a queued start job in remote-fs.target transaction", name)
		}
	}

	if dnsOnline.fragmentPath != dnsOnlineUnitPath {
		t.Errorf("wait-dns-online.service loaded from %q, want test unit %q", dnsOnline.fragmentPath, dnsOnlineUnitPath)
	}

	// Verify full ordering chain:
	// network-online.target & nss-lookup.target -> wait-dns-online.service -> remote-fs-pre.target -> mnt-testnfs.mount -> remote-fs.target
	if !dnsOnline.after["network-online.target"] {
		t.Errorf("wait-dns-online.service missing After=network-online.target (After=%v)", dnsOnline.after)
	}
	if !dnsOnline.after["nss-lookup.target"] {
		t.Errorf("wait-dns-online.service missing After=nss-lookup.target (After=%v)", dnsOnline.after)
	}
	if !dnsOnline.wants["remote-fs-pre.target"] {
		t.Errorf("wait-dns-online.service missing Wants=remote-fs-pre.target (Wants=%v)", dnsOnline.wants)
	}
	if !dnsOnline.before["remote-fs-pre.target"] {
		t.Errorf("wait-dns-online.service missing Before=remote-fs-pre.target (Before=%v)", dnsOnline.before)
	}

	if !remoteFsPre.after["wait-dns-online.service"] {
		t.Errorf("remote-fs-pre.target missing After=wait-dns-online.service (After=%v)", remoteFsPre.after)
	}
	if !remoteFsPre.before[nfsMountUnit] {
		t.Errorf("remote-fs-pre.target missing Before=%s (Before=%v)", nfsMountUnit, remoteFsPre.before)
	}
	if !remoteFsPre.before["remote-fs.target"] {
		t.Errorf("remote-fs-pre.target missing Before=remote-fs.target (Before=%v)", remoteFsPre.before)
	}

	if !nfsMount.after["remote-fs-pre.target"] {
		t.Errorf("%s missing implicit After=remote-fs-pre.target (After=%v)", nfsMountUnit, nfsMount.after)
	}
	if !nfsMount.before["remote-fs.target"] {
		t.Errorf("%s missing Before=remote-fs.target (Before=%v)", nfsMountUnit, nfsMount.before)
	}

	if !remoteFs.after["remote-fs-pre.target"] {
		t.Errorf("remote-fs.target missing After=remote-fs-pre.target (After=%v)", remoteFs.after)
	}
	if !remoteFs.after[nfsMountUnit] {
		t.Errorf("remote-fs.target missing After=%s (After=%v)", nfsMountUnit, remoteFs.after)
	}
}
