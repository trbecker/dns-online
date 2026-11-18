package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bigkevmcd/go-configparser"
)

const DefaultConfigPath = "/etc/default/dns-online"
const DefaultFstabPath = "/etc/fstab"

// Config holds the runtime configuration for dns-online.
type Config struct {
	ConfigPath    string
	Hosts         []string
	UseFstab      bool
	FstabPath     string
	IncludeNoauto bool
	WaitAny       bool
	Timeout       time.Duration
	Interval      time.Duration
	QueryTimeout  time.Duration
	IPv4Only      bool
	IPv6Only      bool
	Quiet         bool
}

// ParseConfig builds a Config from defaults, config file, environment variables, and CLI arguments.
// Precedence: CLI flags > Environment variables > Config file > Defaults.
func ParseConfig(args []string, getenv func(string) string, stderr io.Writer) (*Config, error) {
	cfg := &Config{
		ConfigPath:   DefaultConfigPath,
		FstabPath:    DefaultFstabPath,
		Timeout:      2 * time.Minute,
		Interval:     1 * time.Second,
		QueryTimeout: 5 * time.Second,
	}

	// Pre-scan args/env for custom config file path so we load it before applying env/CLI overrides.
	configPath, configExplicit := detectConfigPath(args, getenv)
	cfg.ConfigPath = configPath

	fileEnv := make(map[string]string)
	if configPath != "" {
		parsed, err := parseEnvFile(configPath)
		if err != nil {
			if configExplicit || !errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("loading config file %s: %w", configPath, err)
			}
		} else {
			fileEnv = parsed
		}
	}

	lookup := func(key string) (string, bool) {
		if val := getenv(key); val != "" {
			return val, true
		}
		if val, ok := fileEnv[key]; ok && val != "" {
			return val, true
		}
		return "", false
	}

	// Apply config file + environment variables
	var envHosts []string
	if val, ok := lookup("DNS_ONLINE_HOSTS"); ok {
		envHosts = splitHosts(val)
	}
	if val, ok := lookup("DNS_ONLINE_FSTAB"); ok {
		b, err := strconv.ParseBool(val)
		if err != nil {
			return nil, fmt.Errorf("invalid DNS_ONLINE_FSTAB %q: %w", val, err)
		}
		cfg.UseFstab = b
	}
	if val, ok := lookup("DNS_ONLINE_FSTAB_PATH"); ok {
		cfg.FstabPath = val
	}
	if val, ok := lookup("DNS_ONLINE_INCLUDE_NOAUTO"); ok {
		b, err := strconv.ParseBool(val)
		if err != nil {
			return nil, fmt.Errorf("invalid DNS_ONLINE_INCLUDE_NOAUTO %q: %w", val, err)
		}
		cfg.IncludeNoauto = b
	}
	if val, ok := lookup("DNS_ONLINE_ANY"); ok {
		b, err := strconv.ParseBool(val)
		if err != nil {
			return nil, fmt.Errorf("invalid DNS_ONLINE_ANY %q: %w", val, err)
		}
		cfg.WaitAny = b
	}
	if val, ok := lookup("DNS_ONLINE_TIMEOUT"); ok {
		d, err := time.ParseDuration(val)
		if err != nil {
			return nil, fmt.Errorf("invalid DNS_ONLINE_TIMEOUT %q: %w", val, err)
		}
		cfg.Timeout = d
	}
	if val, ok := lookup("DNS_ONLINE_INTERVAL"); ok {
		d, err := time.ParseDuration(val)
		if err != nil {
			return nil, fmt.Errorf("invalid DNS_ONLINE_INTERVAL %q: %w", val, err)
		}
		cfg.Interval = d
	}
	if val, ok := lookup("DNS_ONLINE_QUERY_TIMEOUT"); ok {
		d, err := time.ParseDuration(val)
		if err != nil {
			return nil, fmt.Errorf("invalid DNS_ONLINE_QUERY_TIMEOUT %q: %w", val, err)
		}
		cfg.QueryTimeout = d
	}
	if val, ok := lookup("DNS_ONLINE_IPV4"); ok {
		b, err := strconv.ParseBool(val)
		if err != nil {
			return nil, fmt.Errorf("invalid DNS_ONLINE_IPV4 %q: %w", val, err)
		}
		cfg.IPv4Only = b
	}
	if val, ok := lookup("DNS_ONLINE_IPV6"); ok {
		b, err := strconv.ParseBool(val)
		if err != nil {
			return nil, fmt.Errorf("invalid DNS_ONLINE_IPV6 %q: %w", val, err)
		}
		cfg.IPv6Only = b
	}
	if val, ok := lookup("DNS_ONLINE_QUIET"); ok {
		b, err := strconv.ParseBool(val)
		if err != nil {
			return nil, fmt.Errorf("invalid DNS_ONLINE_QUIET %q: %w", val, err)
		}
		cfg.Quiet = b
	}

	// Parse CLI flags
	fs := flag.NewFlagSet("dns-online", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var hostsFlag string
	fs.StringVar(&cfg.ConfigPath, "config", cfg.ConfigPath, "Path to configuration file (env format)")
	fs.StringVar(&hostsFlag, "hosts", "", "Comma or space separated list of hostnames to resolve")
	fs.BoolVar(&cfg.UseFstab, "fstab", cfg.UseFstab, "Auto-discover NFS and CIFS server hostnames from fstab")
	fs.StringVar(&cfg.FstabPath, "fstab-path", cfg.FstabPath, "Path to fstab file")
	fs.BoolVar(&cfg.IncludeNoauto, "include-noauto", cfg.IncludeNoauto, "Include 'noauto' NFS/CIFS entries from fstab")
	fs.BoolVar(&cfg.WaitAny, "any", cfg.WaitAny, "Exit as soon as any target hostname resolves (default: wait for all)")
	fs.DurationVar(&cfg.Timeout, "timeout", cfg.Timeout, "Maximum total time to wait (0 for infinite)")
	fs.DurationVar(&cfg.Interval, "interval", cfg.Interval, "Retry interval between failed resolution attempts")
	fs.DurationVar(&cfg.QueryTimeout, "query-timeout", cfg.QueryTimeout, "Timeout for each individual DNS lookup")
	fs.BoolVar(&cfg.IPv4Only, "4", cfg.IPv4Only, "Resolve IPv4 addresses only")
	fs.BoolVar(&cfg.IPv6Only, "6", cfg.IPv6Only, "Resolve IPv6 addresses only")
	fs.BoolVar(&cfg.Quiet, "quiet", cfg.Quiet, "Suppress informational output")
	fs.BoolVar(&cfg.Quiet, "q", cfg.Quiet, "Suppress informational output (shorthand)")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	if cfg.IPv4Only && cfg.IPv6Only {
		return nil, errors.New("cannot specify both -4 (IPv4 only) and -6 (IPv6 only)")
	}
	if cfg.Interval <= 0 {
		return nil, errors.New("interval must be greater than 0")
	}
	if cfg.QueryTimeout <= 0 {
		return nil, errors.New("query-timeout must be greater than 0")
	}
	if cfg.Timeout < 0 {
		return nil, errors.New("timeout cannot be negative")
	}

	// Collect hosts from CLI (-hosts and positional arguments); if none on CLI, use env/config hosts.
	var cliHosts []string
	if hostsFlag != "" {
		cliHosts = append(cliHosts, splitHosts(hostsFlag)...)
	}
	for _, arg := range fs.Args() {
		cliHosts = append(cliHosts, splitHosts(arg)...)
	}

	var combinedHosts []string
	if len(cliHosts) > 0 {
		combinedHosts = append(combinedHosts, cliHosts...)
	} else {
		combinedHosts = append(combinedHosts, envHosts...)
	}

	// If fstab discovery is enabled, parse fstab and merge hosts.
	if cfg.UseFstab {
		fstabHosts, err := ParseFstabFile(cfg.FstabPath, cfg.IncludeNoauto)
		if err != nil {
			return nil, err
		}
		combinedHosts = append(combinedHosts, fstabHosts...)
	}

	cfg.Hosts = deduplicateStrings(combinedHosts)

	if len(cfg.Hosts) == 0 && !cfg.UseFstab {
		return nil, errors.New("no hostnames specified (provide hostnames via CLI, DNS_ONLINE_HOSTS, or use -fstab)")
	}

	return cfg, nil
}

// Network returns the network string for net.Resolver.LookupIP ("ip", "ip4", or "ip6").
func (c *Config) Network() string {
	if c.IPv4Only {
		return "ip4"
	}
	if c.IPv6Only {
		return "ip6"
	}
	return "ip"
}

func detectConfigPath(args []string, getenv func(string) string) (string, bool) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if arg == "-config" || arg == "--config" {
			if i+1 < len(args) {
				return args[i+1], true
			}
		}
		if strings.HasPrefix(arg, "-config=") {
			return strings.TrimPrefix(arg, "-config="), true
		}
		if strings.HasPrefix(arg, "--config=") {
			return strings.TrimPrefix(arg, "--config="), true
		}
	}
	if envPath := getenv("DNS_ONLINE_CONFIG"); envPath != "" {
		return envPath, true
	}
	return DefaultConfigPath, false
}

// parseEnvFile parses a configuration file (such as /etc/default/dns-online) using go-configparser.
// It supports both INI-style files with section headers and headerless KEY=VALUE environment files.
func parseEnvFile(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	buf.WriteString("[DEFAULT]\n")
	lineNum := 0
	for _, line := range strings.Split(string(raw), "\n") {
		lineNum++
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			buf.WriteString(trimmed)
			buf.WriteByte('\n')
			continue
		}
		if strings.HasPrefix(trimmed, "export ") {
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "export "))
		}
		if strings.Index(trimmed, "=") <= 0 {
			return nil, fmt.Errorf("line %d: expected KEY=VALUE", lineNum)
		}
		buf.WriteString(trimmed)
		buf.WriteByte('\n')
	}

	p, err := configparser.ParseReaderWithOptions(
		&buf,
		configparser.Delimiters("="),
	)
	if err != nil {
		return nil, err
	}

	result := make(map[string]string)
	collect := func(items configparser.Dict) {
		for k, v := range items {
			result[strings.TrimSpace(k)] = cleanConfigValue(v)
		}
	}

	collect(p.Defaults())
	for _, section := range p.Sections() {
		items, err := p.Items(section)
		if err != nil {
			return nil, err
		}
		collect(items)
	}

	return result, nil
}

func cleanConfigValue(val string) string {
	val = strings.TrimSpace(val)
	if len(val) >= 2 && ((val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'')) {
		return val[1 : len(val)-1]
	}
	if idx := strings.Index(val, " #"); idx != -1 {
		val = strings.TrimSpace(val[:idx])
	}
	if idx := strings.Index(val, "\t#"); idx != -1 {
		val = strings.TrimSpace(val[:idx])
	}
	return val
}

func splitHosts(raw string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	}) {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func deduplicateStrings(in []string) []string {
	var out []string
	seen := make(map[string]struct{}, len(in))
	for _, s := range in {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	return out
}
