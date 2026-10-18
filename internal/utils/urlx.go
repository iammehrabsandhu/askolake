package utils

import (
	"net/url"
	"strings"
)

// StripFragment removes the #fragment from u.
func StripFragment(u string) string {
	if i := strings.IndexByte(u, '#'); i >= 0 {
		return u[:i]
	}
	return u
}

// GitHubSlug converts a Markdown heading into a GitHub-style anchor.
func GitHubSlug(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(heading) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	return b.String()
}

// HostOf returns the lowercased host of u, or "" when u does not parse.
func HostOf(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return strings.ToLower(p.Hostname())
}

// IsHTTPURL reports whether u is an absolute http(s) URL with a host.
func IsHTTPURL(u string) bool {
	p, err := url.Parse(u)
	if err != nil {
		return false
	}
	return (p.Scheme == "http" || p.Scheme == "https") && p.Host != ""
}

// IsTrustedHost reports whether u points at olake.io or the datazip-inc GitHub org.
func IsTrustedHost(u string) bool {
	p, err := url.Parse(u)
	if err != nil || (p.Scheme != "http" && p.Scheme != "https") {
		return false
	}
	switch strings.ToLower(p.Hostname()) {
	case "olake.io", "www.olake.io":
		return true
	case "github.com":
		return strings.HasPrefix(p.Path, "/datazip-inc/")
	}
	return false
}
