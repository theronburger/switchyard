package marketplacecontrol

import (
	"net/url"
	"strings"
)

func normalizeRemote(contents []byte) (string, bool) {
	remoteBytes, valid := parseOneLine(contents)
	if !valid {
		return "", false
	}
	remote := string(remoteBytes)
	if strings.Contains(remote, "://") {
		return normalizeURLRemote(remote)
	}
	return normalizeSCPRemote(remote)
}

func normalizeURLRemote(remote string) (string, bool) {
	parsed, err := url.Parse(remote)
	if err != nil || parsed.Hostname() == "" || parsed.Path == "" {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	repositoryPath := normalizedRepositoryPath(parsed.Path)
	if repositoryPath == "" {
		return "", false
	}
	if host == "github.com" {
		return strings.ToLower(repositoryPath), true
	}
	return host + "/" + repositoryPath, true
}

func normalizeSCPRemote(remote string) (string, bool) {
	separator := strings.IndexByte(remote, ':')
	if separator < 1 || separator == len(remote)-1 {
		return "", false
	}
	hostPart := remote[:separator]
	if userSeparator := strings.LastIndexByte(hostPart, '@'); userSeparator >= 0 {
		hostPart = hostPart[userSeparator+1:]
	}
	host := strings.ToLower(strings.TrimSpace(hostPart))
	repositoryPath := normalizedRepositoryPath(remote[separator+1:])
	if host == "" || repositoryPath == "" {
		return "", false
	}
	if host == "github.com" {
		return strings.ToLower(repositoryPath), true
	}
	return host + "/" + repositoryPath, true
}

func normalizedRepositoryPath(repositoryPath string) string {
	repositoryPath = strings.Trim(repositoryPath, "/")
	repositoryPath = strings.TrimSuffix(repositoryPath, ".git")
	if repositoryPath == "" || strings.Contains(repositoryPath, "..") ||
		strings.ContainsAny(repositoryPath, "?#\x00\r\n") {
		return ""
	}
	return repositoryPath
}
