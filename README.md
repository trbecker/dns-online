# dns-online

`dns-online` is a lightweight Go utility and systemd `oneshot` service that blocks until one or more target hostnames can be resolved via the system resolver (`libc`/NSS `getaddrinfo`).

It is specifically designed to order remote filesystem mounts (`nfs`, `nfs4`, `cifs`, `smb3`, `smbfs`) in `/etc/fstab` (or systemd `.mount` units) after working DNS resolution is available during boot, preventing early-boot mount failures when `network-online.target` is reached before DNS resolvers or upstream nameservers are ready.

## Features

- **System Resolver Integration**: Built with `-tags netcgo` so lookups use the system `getaddrinfo(3)` / NSS stack (`/etc/nsswitch.conf`, `systemd-resolved`, `/etc/hosts`, mDNS, etc.), matching `mount.nfs` and `mount.cifs` behavior.
- **Automatic `/etc/fstab` Discovery**: Optionally parses `/etc/fstab` (`-fstab` / `DNS_ONLINE_FSTAB=true`) to extract all `nfs`, `nfs4`, `cifs`, `smb3`, and `smbfs` server hostnames automatically (skipping `noauto` entries by default).
- **Flexible Configuration**: Supports CLI flags/arguments, environment variables, and `/etc/default/dns-online` (`CLI > Environment > Config File > Defaults`).
- **Systemd Unit Included**: Orders `After=network-online.target nss-lookup.target` and `Before=remote-fs-pre.target`, ensuring all automatic network mounts wait for DNS resolution.

## Building & Testing

```bash
make build
make test
```

## Installation (systemd)

```bash
sudo make install
sudo systemctl daemon-reload
sudo systemctl enable --now dns-online.service
```

By default, `/etc/default/dns-online` sets `DNS_ONLINE_FSTAB=true`, which automatically discovers and waits for all `nfs`/`nfs4` server hostnames in `/etc/fstab`. You can also add explicit hostnames via `DNS_ONLINE_HOSTS` in `/etc/default/dns-online`.

## CLI Usage

```text
Usage of dns-online:
  -4    Resolve IPv4 addresses only
  -6    Resolve IPv6 addresses only
  -any
        Exit as soon as any target hostname resolves (default: wait for all)
  -config string
        Path to configuration file (env format) (default "/etc/default/dns-online")
  -fstab
        Auto-discover NFS server hostnames from fstab
  -fstab-path string
        Path to fstab file (default "/etc/fstab")
  -hosts string
        Comma or space separated list of hostnames to resolve
  -include-noauto
        Include 'noauto' NFS entries from fstab
  -interval duration
        Retry interval between failed resolution attempts (default 1s)
  -q    Suppress informational output (shorthand)
  -query-timeout duration
        Timeout for each individual DNS lookup (default 5s)
  -quiet
        Suppress informational output
  -timeout duration
        Maximum total time to wait (0 for infinite) (default 2m0s)
```

### Examples

Wait for all NFS servers in `/etc/fstab` to resolve (up to 2 minutes):
```bash
dns-online -fstab
```

Wait for specific hosts passed as arguments:
```bash
dns-online nas1.example.com nas2.example.com
```

Wait for both `/etc/fstab` NFS servers and an additional hostname, with a 60s timeout:
```bash
dns-online -fstab -timeout 60s ipa.example.com
```
