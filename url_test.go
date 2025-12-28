// Package main implements a Web-of-Trust (WoT) based Nostr relay
// with reputation-driven rate limiting. It enforces community spam-protection
// using external trust scores, with rate limits determined by a pubkey's reputation.
package main

import (
	"testing"
)

func TestContainsURL(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected bool
	}{
		// HTTP/HTTPS URLs with protocol
		{
			name:     "http URL",
			content:  "Check out http://example.com",
			expected: true,
		},
		{
			name:     "https URL",
			content:  "Visit https://example.com/path?query=value",
			expected: true,
		},
		{
			name:     "https URL with port",
			content:  "https://example.com:8080/path",
			expected: true,
		},
		{
			name:     "https URL with complex query",
			content:  "https://duckduckgo.com/?q=DuckDuckGo+AI+Chat&ia=chat&duckai=1",
			expected: true,
		},

		// www.* URLs
		{
			name:     "www URL",
			content:  "Go to www.example.com",
			expected: true,
		},
		{
			name:     "www URL with path",
			content:  "Visit www.example.com/path/to/page",
			expected: true,
		},
		{
			name:     "www URL with port",
			content:  "www.example.com:8080",
			expected: true,
		},

		// Bare domains
		{
			name:     "bare domain",
			content:  "Visit example.com",
			expected: true,
		},
		{
			name:     "bare domain with path",
			content:  "example.com/path",
			expected: true,
		},
		{
			name:     "bare domain with query",
			content:  "example.com?q=test",
			expected: true,
		},
		{
			name:     "bare domain with port",
			content:  "example.com:8080",
			expected: true,
		},
		{
			name:     "subdomain",
			content:  "sub.example.com",
			expected: true,
		},
		{
			name:     "multi-level subdomain",
			content:  "a.b.c.example.com",
			expected: true,
		},

		// Non-HTTP schemes (should NOT match)
		{
			name:     "nostr scheme",
			content:  "nostr:npub1...",
			expected: false,
		},
		{
			name:     "bitcoin scheme",
			content:  "bitcoin:bc1q...",
			expected: false,
		},
		{
			name:     "mailto scheme",
			content:  "mailto:test@example.com",
			expected: false,
		},
		{
			name:     "ipfs scheme",
			content:  "ipfs://Qm...",
			expected: false,
		},

		// Localhost and local network (should NOT match)
		{
			name:     "localhost",
			content:  "http://localhost:8080",
			expected: false,
		},
		{
			name:     "127.0.0.1",
			content:  "http://127.0.0.1:8080",
			expected: false,
		},
		{
			name:     "192.168.x.x",
			content:  "http://192.168.1.1",
			expected: false,
		},
		{
			name:     "10.x.x.x",
			content:  "http://10.0.0.1",
			expected: false,
		},
		{
			name:     "private domain (dot local) is not a URL",
			content:  "printer.local/status",
			expected: false,
		},

		// Edge cases - text that should NOT match
		{
			name:     "plain text",
			content:  "Just some plain text without URLs",
			expected: false,
		},
		{
			name:     "single word",
			content:  "hello",
			expected: false,
		},
		{
			name:     "email address",
			content:  "Contact me at test@example.com",
			expected: false, // email addresses are not URLs
		},
		{
			name:     "version number",
			content:  "Version 1.2.3",
			expected: false,
		},
		{
			name:     "IP address without protocol",
			content:  "8.8.8.8",
			expected: false,
		},

		// Multiple URLs in content
		{
			name:     "multiple URLs",
			content:  "Visit https://example.com and www.test.org",
			expected: true,
		},

		// URL at start/end of content
		{
			name:     "URL at start",
			content:  "https://example.com is a great site",
			expected: true,
		},
		{
			name:     "URL at end",
			content:  "Check out https://example.com",
			expected: true,
		},
		{
			name:     "URL only",
			content:  "https://example.com",
			expected: true,
		},

		// Common TLDs
		{
			name:     "com domain",
			content:  "example.com",
			expected: true,
		},
		{
			name:     "org domain",
			content:  "example.org",
			expected: true,
		},
		{
			name:     "net domain",
			content:  "example.net",
			expected: true,
		},
		{
			name:     "io domain",
			content:  "example.io",
			expected: true,
		},
		{
			name:     "co.uk domain",
			content:  "example.co.uk",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ContainsURL(tt.content)
			if result != tt.expected {
				t.Errorf("ContainsURL(%q) = %v, expected %v", tt.content, result, tt.expected)
			}
		})
	}
}

func TestContainsURL_EdgeCases(t *testing.T) {
	// Test that the regex doesn't match single words that look like domains
	// but are likely just text
	tests := []struct {
		name     string
		content  string
		expected bool
	}{
		{
			name:     "single word with dot in middle",
			content:  "foo.bar",
			expected: true, // valid domain pattern
		},
		{
			name:     "word with dot at end",
			content:  "example.",
			expected: false,
		},
		{
			name:     "word with dot at start",
			content:  ".example",
			expected: false,
		},
		{
			name:     "domain with hyphen",
			content:  "my-example.com",
			expected: true,
		},
		{
			name:     "domain with numbers",
			content:  "example123.com",
			expected: true,
		},
		{
			name:     "domain with underscore",
			content:  "example_test.com",
			expected: false, // underscores are not valid in domain names
		},
		{
			name:     "email should not match",
			content:  "test@example.com",
			expected: false,
		},
		{
			name:     "domain in parentheses",
			content:  "(example.com)",
			expected: true,
		},
		{
			name:     "domain with trailing punctuation",
			content:  "example.com.",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ContainsURL(tt.content)
			if result != tt.expected {
				t.Errorf("ContainsURL(%q) = %v, expected %v", tt.content, result, tt.expected)
			}
		})
	}
}
