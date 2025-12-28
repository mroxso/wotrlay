// Package main implements a Web-of-Trust (WoT) based Nostr relay
// with reputation-driven rate limiting. It enforces community spam-protection
// using external trust scores, with rate limits determined by a pubkey's reputation.
package main

import (
	"net"
	"regexp"
	"strings"
)

// urlCandidateRegex finds URL-ish substrings in text content.
//
// It intentionally aims to be:
//   - Simple and fast (RE2; no catastrophic backtracking)
//   - Conservative on what it matches (to reduce false positives)
//
// We keep validation (e.g. localhost/private IP exclusion) in Go code because
// Go's regexp engine (RE2) does not support lookahead/lookbehind.
var urlCandidateRegex = regexp.MustCompile(`(?i)(?:https?://|www\.)[^\s]+|(?:[a-z0-9-]+\.)+[a-z]{2,}(?:/[^\s]*)?`)

func isDomainChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '-' || b == '_'
}

// ContainsURL returns true if the content contains a URL.
// This is used to enforce URL policy for low-trust users.
func ContainsURL(content string) bool {
	if content == "" {
		return false
	}

	// Avoid FindAll* to keep allocations minimal on the hot path.
	for off := 0; off < len(content); {
		loc := urlCandidateRegex.FindStringIndex(content[off:])
		if loc == nil {
			return false
		}
		start := off + loc[0]
		end := off + loc[1]
		off = end

		// Skip matches preceded by '@' (emails) or domain characters.
		// This prevents matching "test.com" within "example_test.com".
		if start > 0 {
			prev := content[start-1]
			if prev == '@' || isDomainChar(prev) {
				continue
			}
		}

		candidate := strings.Trim(content[start:end], "()[]{}<>,.\"'`")
		if candidate == "" {
			continue
		}

		// Reject underscores (not valid in DNS hostnames).
		if strings.IndexByte(candidate, '_') >= 0 {
			continue
		}

		if isAllowedURLCandidate(candidate) {
			return true
		}
	}

	return false
}

func isAllowedURLCandidate(candidate string) bool {
	// Only treat http/https + www.* + bare domains as URLs.
	// (Non-HTTP schemes are ignored by construction: the regex doesn't match them.)

	// Extract host (strip scheme, path, query, fragment, and port).
	// Keep parsing simple to reduce allocations.
	s := candidate
	if len(s) >= 7 && strings.EqualFold(s[:7], "http://") {
		s = s[7:]
	} else if len(s) >= 8 && strings.EqualFold(s[:8], "https://") {
		s = s[8:]
	}

	// Cut at first path/query/fragment delimiter.
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	// Strip userinfo if present (rare, but possible in URLs).
	if at := strings.LastIndexByte(s, '@'); at >= 0 {
		s = s[at+1:]
	}
	// Strip port.
	host := s
	if h, _, err := net.SplitHostPort(s); err == nil {
		host = h
	} else {
		// If it looks like host:port without brackets, split on last ':'
		// (net.SplitHostPort requires a port; this is just a best-effort).
		if c := strings.LastIndexByte(s, ':'); c >= 0 {
			port := s[c+1:]
			ok := port != ""
			for i := 0; ok && i < len(port); i++ {
				b := port[i]
				ok = b >= '0' && b <= '9'
			}
			if ok {
				host = s[:c]
			}
		}
	}

	if host == "" {
		return false
	}
	hostLower := strings.ToLower(host)
	if hostLower == "localhost" {
		return false
	}
	if strings.HasSuffix(hostLower, ".local") {
		return false
	}

	if ip := net.ParseIP(host); ip != nil {
		// Block loopback + private + link-local + unspecified.
		return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified())
	}

	// Minimal hostname sanity: must contain at least one dot.
	return strings.Contains(hostLower, ".")
}
