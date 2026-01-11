package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"

	"github.com/nbd-wtf/go-nostr/nip11"
)

// generateFavicon creates a simple 16x16 PNG favicon with a blue background
func generateFavicon() []byte {
	// Create a 16x16 image
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))

	// Fill with a nice blue color (#3498db)
	bgColor := color.RGBA{52, 152, 219, 255}
	for y := range 16 {
		for x := range 16 {
			img.Set(x, y, bgColor)
		}
	}

	// Add a simple white bucket shape in the center
	textColor := color.RGBA{255, 255, 255, 255}
	// Bucket shape: wider at top, narrower at bottom
	positions := []struct{ x, y int }{
		// Top rim (wider)
		{3, 5}, {4, 5}, {5, 5}, {6, 5}, {7, 5}, {8, 5}, {9, 5}, {10, 5}, {11, 5}, {12, 5},
		// Left side (slanted inward)
		{4, 6}, {4, 7}, {5, 8}, {5, 9}, {6, 10},
		// Right side (slanted inward)
		{11, 6}, {11, 7}, {10, 8}, {10, 9}, {9, 10},
		// Bottom (narrower)
		{6, 10}, {7, 10}, {8, 10}, {9, 10},
		// Handle
		{3, 6}, {2, 7}, {2, 8},
	}

	for _, pos := range positions {
		img.Set(pos.x, pos.y, textColor)
	}

	// Encode to PNG
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

// serveFavicon handles favicon requests
func serveFavicon() http.HandlerFunc {
	favicon := generateFavicon()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=86400") // Cache for 1 day
		w.WriteHeader(http.StatusOK)
		w.Write(favicon)
	}
}

// serveHTMLPage handles HTTP requests for the root path and serves a simple HTML page
func serveHTMLPage(cfg Config, _ nip11.RelayInformationDocument) http.HandlerFunc {
	// Pre-render the HTML page once at startup
	html := `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>` + cfg.RelayName + ` - Nostr Relay</title>
    <link rel="icon" type="image/png" href="/favicon.ico">
    <style>
        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
            max-width: 800px;
            margin: 50px auto;
            padding: 20px;
            background: #f5f5f5;
            color: #333;
        }
        .container {
            background: white;
            padding: 30px;
            border-radius: 8px;
            box-shadow: 0 2px 4px rgba(0,0,0,0.1);
        }
        h1 {
            color: #2c3e50;
            margin-top: 0;
        }
        .info-section {
            margin: 20px 0;
        }
        .info-label {
            font-weight: bold;
            color: #555;
        }
        .description {
            line-height: 1.6;
            color: #666;
        }
        .supported-nips {
            display: flex;
            flex-wrap: wrap;
            gap: 8px;
            margin: 10px 0;
        }
        .nip-badge {
            background: #3498db;
            color: white;
            padding: 4px 8px;
            border-radius: 4px;
            font-size: 12px;
            font-weight: bold;
        }
        .footer {
            margin-top: 30px;
            padding-top: 20px;
            border-top: 1px solid #eee;
            font-size: 14px;
            color: #888;
        }
        .contact {
            margin-top: 10px;
        }
        .contact a {
            color: #3498db;
            text-decoration: none;
        }
        .contact a:hover {
            text-decoration: underline;
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>Welcome to ` + cfg.RelayName + `</h1>
        
        <div class="info-section">
            <p class="description">` + cfg.RelayDescription + `</p>
        </div>

        <div class="info-section">
            <div class="info-label">Software:</div>
            <div>` + cfg.Software + ` (v` + cfg.Version + `)</div>
        </div>

        <div class="info-section">
            <div class="info-label">Supported NIPs:</div>
            <div class="supported-nips">
                <span class="nip-badge">NIP-01</span>
                <span class="nip-badge">NIP-11</span>
            </div>
        </div>`

	// Add pubkey if configured
	if cfg.RelayPubKey != "" {
		html += `
        <div class="info-section">
            <div class="info-label">Relay Public Key:</div>
            <div style="font-family: monospace; font-size: 12px; word-break: break-all;">` + cfg.RelayPubKey + `</div>
        </div>`
	}

	// Add contact if configured
	if cfg.RelayContact != "" {
		html += `
        <div class="info-section">
            <div class="info-label">Contact:</div>
            <div class="contact">` + cfg.RelayContact + `</div>
        </div>`
	}

	html += `
        <div class="footer">
            <p>This is a Nostr relay implementing NIP-11 (Relay Information Document).</p>
            <p>Connect using any Nostr client with the relay URL: <code>ws://localhost:3334</code></p>
        </div>
    </div>
</body>
</html>`

	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(html))
	}
}
