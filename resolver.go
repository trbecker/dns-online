package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// HostResolver abstracts DNS/NSS hostname lookup for testing and system resolution.
type HostResolver interface {
	LookupIP(ctx context.Context, network, host string) ([]net.IP, error)
}

// SystemResolver uses Go's net.Resolver configured to prefer the system libc/NSS resolver (getaddrinfo).
func SystemResolver() HostResolver {
	return &net.Resolver{
		PreferGo: false,
	}
}

// WaitForDNS blocks until the configured hostnames resolve (all by default, or any if cfg.WaitAny is set),
// or until ctx is canceled or cfg.Timeout elapses.
func WaitForDNS(ctx context.Context, cfg *Config, resolver HostResolver, logf func(format string, args ...any)) error {
	if len(cfg.Hosts) == 0 {
		logf("No target hostnames to resolve; succeeding immediately.")
		return nil
	}

	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}

	network := cfg.Network()
	resolved := make(map[string]bool, len(cfg.Hosts))
	lastErr := make(map[string]error, len(cfg.Hosts))

	modeDesc := "all"
	if cfg.WaitAny {
		modeDesc = "any"
	}
	logf("Waiting for DNS resolution (%s of %d host(s): %s)...", modeDesc, len(cfg.Hosts), strings.Join(cfg.Hosts, ", "))

	attempt := 0
	for {
		attempt++
		for _, host := range cfg.Hosts {
			if resolved[host] {
				continue
			}

			// Check if parent context already expired before starting another lookup
			if err := ctx.Err(); err != nil {
				return formatTimeoutError(err, cfg.Hosts, resolved, lastErr)
			}

			queryCtx, cancel := context.WithTimeout(ctx, cfg.QueryTimeout)
			ips, err := resolver.LookupIP(queryCtx, network, host)
			cancel()

			if err == nil && len(ips) > 0 {
				resolved[host] = true
				delete(lastErr, host)
				logf("Resolved %s -> %s", host, formatIPs(ips))
				if cfg.WaitAny {
					return nil
				}
			} else {
				if err != nil {
					lastErr[host] = err
				} else {
					lastErr[host] = errors.New("no addresses returned")
				}
			}
		}

		if len(resolved) == len(cfg.Hosts) {
			logf("All %d hostname(s) resolved successfully.", len(cfg.Hosts))
			return nil
		}

		// Wait for next retry interval or context termination
		timer := time.NewTimer(cfg.Interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return formatTimeoutError(ctx.Err(), cfg.Hosts, resolved, lastErr)
		case <-timer.C:
		}
	}
}

func formatIPs(ips []net.IP) string {
	strs := make([]string, len(ips))
	for i, ip := range ips {
		strs[i] = ip.String()
	}
	return strings.Join(strs, ", ")
}

func formatTimeoutError(ctxErr error, hosts []string, resolved map[string]bool, lastErr map[string]error) error {
	var unresolved []string
	for _, h := range hosts {
		if !resolved[h] {
			if err, ok := lastErr[h]; ok {
				unresolved = append(unresolved, fmt.Sprintf("%s (%v)", h, err))
			} else {
				unresolved = append(unresolved, h)
			}
		}
	}
	return fmt.Errorf("failed waiting for DNS resolution (%v): unresolved: %s", ctxErr, strings.Join(unresolved, "; "))
}
