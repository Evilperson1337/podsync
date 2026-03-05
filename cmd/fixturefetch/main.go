package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	defaultOutDir    = "pkg/builder/testdata/rumble"
	fixtureUserAgent = "PodsyncFixtureFetch/1.0 (+https://github.com/mxpv/podsync)"
)

func main() {
	var (
		outDir string
		proxy  string
	)
	flag.StringVar(&outDir, "out", defaultOutDir, "output directory")
	flag.StringVar(&proxy, "proxy", "", "http proxy URL")
	flag.Parse()

	urls := flag.Args()
	if len(urls) == 0 {
		fmt.Println("usage: fixturefetch --out <dir> [--proxy <url>] <url> [url...]")
		os.Exit(1)
	}

	client := &http.Client{Timeout: 20 * time.Second}
	if proxy != "" {
		proxyURL, err := url.Parse(proxy)
		if err != nil {
			fmt.Printf("invalid proxy: %v\n", err)
			os.Exit(1)
		}
		client.Transport = &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Printf("failed to create output dir: %v\n", err)
		os.Exit(1)
	}

	for _, raw := range urls {
		if err := fetchFixture(client, outDir, raw); err != nil {
			fmt.Printf("failed to fetch %s: %v\n", raw, err)
			os.Exit(1)
		}
	}
}

func fetchFixture(client *http.Client, outDir string, rawURL string) error {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", fixtureUserAgent)
	req.Header.Set("Accept", "text/html")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	cleaned := scrubFixture(body)
	name := sanitizeFilename(rawURL) + ".html"
	path := filepath.Join(outDir, name)
	if err := os.WriteFile(path, cleaned, 0o644); err != nil {
		return err
	}

	headersPath := filepath.Join(outDir, sanitizeFilename(rawURL)+".headers")
	if err := os.WriteFile(headersPath, []byte(formatHeaders(resp.Header)), 0o644); err != nil {
		return err
	}

	fmt.Printf("saved %s\n", path)
	return nil
}

func scrubFixture(input []byte) []byte {
	// Remove large inline scripts to stabilize fixtures
	scriptRe := regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
	cleaned := scriptRe.ReplaceAll(input, []byte(""))

	// Normalize timestamps in data attributes (best-effort)
	stampRe := regexp.MustCompile(`data-[a-zA-Z0-9_-]+="\d{10,}"`)
	cleaned = stampRe.ReplaceAll(cleaned, []byte(""))

	return bytes.TrimSpace(cleaned)
}

func formatHeaders(header http.Header) string {
	var builder strings.Builder
	for key, values := range header {
		for _, value := range values {
			builder.WriteString(key)
			builder.WriteString(": ")
			builder.WriteString(value)
			builder.WriteString("\n")
		}
	}
	return builder.String()
}

func sanitizeFilename(raw string) string {
	result := strings.ToLower(raw)
	replacer := strings.NewReplacer(
		"https://", "",
		"http://", "",
		"/", "_",
		"?", "_",
		"&", "_",
		"=", "_",
		":", "_",
	)
	result = replacer.Replace(result)
	result = strings.Trim(result, "_")
	return result
}
