package main

import (
	"fmt"
	"io"
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
// Supports standard "host:/path", "[ipv6]:/path", and comma-separated failover "host1,host2:/path".
func extractNFSHosts(spec string) []string {
	spec = strings.TrimSpace(spec)
	if spec == "" {
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
		if rawHost != "" && !strings.Contains(rawHost, "@") {
			result = append(result, rawHost)
		}
	}
	return result
}

// extractCIFSHosts extracts the server hostname from a CIFS/SMB mount spec.
// Supports "//host/share", "//[ipv6]/share", and "\\host\share".
func extractCIFSHosts(spec string) []string {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil
	}

	// Normalize backslashes (UNC paths like \\server\share) to forward slashes
	normalized := strings.ReplaceAll(spec, `\`, `/`)
	if !strings.HasPrefix(normalized, "//") {
		return nil
	}

	rest := normalized[2:]
	slashIdx := strings.Index(rest, "/")
	if slashIdx <= 0 {
		return nil
	}

	hostPart := rest[:slashIdx]
	if strings.Contains(hostPart, "@") {
		return nil
	}

	// Handle bracketed IPv6 literal: //[2001:db8::20]/share or //[2001:db8::20]:445/share
	if strings.HasPrefix(hostPart, "[") {
		endBracket := strings.Index(hostPart, "]")
		if endBracket > 1 {
			return []string{hostPart[1:endBracket]}
		}
		return nil
	}

	// Strip optional :port if present
	if colonIdx := strings.Index(hostPart, ":"); colonIdx != -1 {
		hostPart = hostPart[:colonIdx]
	}

	if hostPart != "" {
		return []string{hostPart}
	}
	return nil
}
