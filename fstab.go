package main

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	fstab "github.com/deniswernert/go-fstab"
)

// ParseFstabFile reads an fstab file from the given path and extracts unique NFS/CIFS server hostnames.
func ParseFstabFile(path string, includeNoauto bool) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening fstab %s: %w", path, err)
	}
	defer f.Close()
	return ParseFstab(f, includeNoauto)
}

// ParseFstab extracts unique remote server hostnames (NFS and CIFS/SMB) from an fstab reader.
func ParseFstab(r io.Reader, includeNoauto bool) ([]string, error) {
	mounts, err := fstab.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("parsing fstab: %w", err)
	}

	var hosts []string
	seen := make(map[string]struct{})

	for _, mount := range mounts {
		spec := unescapeFstabField(mount.Spec)
		fsType := strings.ToLower(mount.VfsType)

		var extracted []string
		switch fsType {
		case "nfs", "nfs4":
			extracted = extractNFSHosts(spec)
		case "cifs", "smb3", "smbfs":
			extracted = extractCIFSHosts(spec)
		default:
			continue
		}

		if !includeNoauto {
			if _, hasNoauto := mount.MntOps["noauto"]; hasNoauto {
				continue
			}
		}

		for _, h := range extracted {
			if h == "" {
				continue
			}
			if _, exists := seen[h]; !exists {
				seen[h] = struct{}{}
				hosts = append(hosts, h)
			}
		}
	}

	return hosts, nil
}

func unescapeFstabField(s string) string {
	r := strings.NewReplacer(
		`\040`, " ",
		`\011`, "\t",
		`\012`, "\n",
		`\134`, `\`,
	)
	return r.Replace(s)
}

// extractNFSHosts extracts one or more server hostnames from an NFS mount spec.
// Supports standard "host:/path", "[ipv6]:/path", comma-separated failover "host1,host2:/path",
// and URL-style "nfs://host[:port]/path".
func extractNFSHosts(spec string) []string {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil
	}

	// Handle URL-style nfs:// or nfs4://
	if strings.HasPrefix(spec, "nfs://") || strings.HasPrefix(spec, "nfs4://") {
		u, err := url.Parse(spec)
		if err == nil && u.Hostname() != "" {
			return []string{u.Hostname()}
		}
		return nil
	}

	// Handle bracketed IPv6 literal: [2001:db8::1]:/export
	if strings.HasPrefix(spec, "[") {
		endBracket := strings.Index(spec, "]")
		if endBracket > 1 && len(spec) > endBracket+1 && spec[endBracket+1] == ':' {
			return []string{spec[1:endBracket]}
		}
		return nil
	}

	// Standard host:/export or host1,host2:/export
	colonIdx := strings.Index(spec, ":")
	if colonIdx <= 0 {
		return nil
	}

	hostPart := spec[:colonIdx]
	var result []string
	for _, rawHost := range strings.Split(hostPart, ",") {
		rawHost = strings.TrimSpace(rawHost)
		// Strip optional mount-specific user@ prefix if present
		if atIdx := strings.LastIndex(rawHost, "@"); atIdx != -1 {
			rawHost = rawHost[atIdx+1:]
		}
		if rawHost != "" {
			result = append(result, rawHost)
		}
	}
	return result
}

// extractCIFSHosts extracts the server hostname from a CIFS/SMB mount spec.
// Supports "//host/share", "//[ipv6]/share", "\\host\share", and "smb://host/share" / "cifs://host/share".
func extractCIFSHosts(spec string) []string {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil
	}

	// Normalize backslashes (UNC paths like \\server\share) to forward slashes
	normalized := strings.ReplaceAll(spec, `\`, `/`)

	if strings.HasPrefix(normalized, "smb://") || strings.HasPrefix(normalized, "cifs://") {
		u, err := url.Parse(normalized)
		if err == nil && u.Hostname() != "" {
			return []string{u.Hostname()}
		}
		return nil
	}

	if strings.HasPrefix(normalized, "//") {
		u, err := url.Parse("cifs:" + normalized)
		if err == nil && u.Hostname() != "" {
			return []string{u.Hostname()}
		}
	}

	return nil
}
