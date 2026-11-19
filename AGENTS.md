# AGENTS.md

## Project Overview

`wait-dns-online` is a Go CLI utility and `systemd` oneshot service that blocks until target hostnames resolve via the system's libc/NSS resolver (`getaddrinfo`). Its primary purpose is ordering `nfs`, `nfs4`, and `cifs`/`smb3` mounts after working DNS resolution is available during system boot (`Before=remote-fs-pre.target`, `After=network-online.target nss-lookup.target`).

## Architecture & Key Files

- `main.go`: Entrypoint (`main` and testable `run`), signal handling (`SIGINT`/`SIGTERM`), logging, and exit code mapping (`0` = resolved, `1` = lookup/timeout failure, `2` = configuration error).
- `config.go`: Configuration parsing and validation using `github.com/bigkevmcd/go-configparser` for config files. Implements strict precedence:
  `CLI flags/args > Environment variables (WAIT_DNS_ONLINE_*) > Config file (/etc/default/wait-dns-online) > Defaults`.
- `fstab.go`: Parses `/etc/fstab` (`ParseFstab` / `ParseFstabFile`) using `github.com/deniswernert/go-fstab` to extract unique server hostnames from `nfs`, `nfs4`, `cifs`, `smb3`, and `smbfs` entries. Ignores `noauto` entries unless `-include-noauto` (`WAIT_DNS_ONLINE_INCLUDE_NOAUTO=true`) is enabled.
- `resolver.go`: Implements `HostResolver` and `WaitForDNS`. Uses `&net.Resolver{PreferGo: false}` so lookups go through libc/NSS when built with `-tags netcgo`.
- `main_test.go`: Unit tests covering `fstab` parsing, configuration precedence, retry loops, `-any` vs all-hosts mode, and timeouts.
- `integration_test.go`: Integration tests (`-tags integration`) exercising the compiled `netcgo` binary, real libc/NSS lookups, `SIGTERM` handling, an in-process UDP DNS server with transient `SERVFAIL` recovery, and `systemd-analyze verify`.
- `systemd/wait-dns-online.service`: Systemd oneshot unit ordered before `remote-fs-pre.target`.
- `systemd/wait-dns-online.default`: Default environment config installed to `/etc/default/wait-dns-online`.
- `Makefile`: Standard build, static build, test, and install targets.

## Build & Test Commands

```bash
# Run unit tests with the race detector
make test

# Run integration tests (builds and executes the netcgo binary and a local UDP DNS server)
make test-integration

# Build binary using libc/NSS resolver (-tags netcgo)
make build

# Build static pure-Go binary (CGO_ENABLED=0, bypasses NSS modules)
make build-static
```

## Development Guidelines & Invariants

1. **Minimal Dependencies**: Keep external dependencies in `go.mod` minimal (`github.com/deniswernert/go-fstab` for `/etc/fstab` parsing and `github.com/bigkevmcd/go-configparser` for configuration file parsing).
2. **System Resolver Compatibility**: Default builds (`make build`) must retain `-tags netcgo` and `PreferGo: false` so hostname resolution matches `mount.nfs` (`getaddrinfo` / `/etc/nsswitch.conf` / `systemd-resolved` / `/etc/hosts`).
3. **Testability**: Keep filesystem paths (`FstabPath`, `ConfigPath`), environment lookup (`getenv func(string) string`), and DNS resolution (`HostResolver` interface) injectable so unit tests never depend on host network state or `/etc` files.
4. **Configuration Parity**: When adding a new option, expose it consistently across:
   - CLI flag in `ParseConfig` (`config.go`)
   - `WAIT_DNS_ONLINE_*` environment variable / config file key (`config.go`)
   - Documented example in `systemd/wait-dns-online.default` and `README.md`
   - Unit tests in `main_test.go`
5. **Graceful Boot Behavior**: When `-fstab` (`WAIT_DNS_ONLINE_FSTAB=true`) is enabled and `/etc/fstab` contains no active NFS mounts, `wait-dns-online` must exit `0` immediately so `wait-dns-online.service` never blocks boot on hosts without NFS mounts.
