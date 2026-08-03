package mcp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"testing"
)

// FuzzHandleMessage fuzzes the JSON-RPC decoder.
//
// The MCP server reads newline-delimited JSON from a client it does not control, so
// every message on that wire is untrusted input. A panic here kills the server and
// takes the agent session with it.
func FuzzHandleMessage(f *testing.F) {
	for _, seed := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		// Note: no seed here performs a real crawl. A tools/call for
		// scrapegoat_crawl with a reachable URL fetches the live internet, which
		// under the fuzzer reads as a hung process — the target exercises the
		// decoder, and handler behaviour is covered by ssrf_test.go.
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"scrapegoat_crawl","arguments":{"url":"file:///etc/passwd"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		// Unknown method, which must be an error response rather than a crash.
		`{"jsonrpc":"2.0","id":4,"method":"does/not/exist"}`,
		// Structural malformations.
		`{"jsonrpc":"2.0","id":`,
		`{}`,
		`[]`,
		`null`,
		`"a string"`,
		``,
		`{"jsonrpc":"1.0","id":1,"method":"x"}`,
		// id of every JSON type — a common source of unmarshalling assumptions.
		`{"jsonrpc":"2.0","id":"string-id","method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":null,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":{"nested":"object"},"method":"tools/list"}`,
		// params of the wrong shape for the method.
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":[]}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":123}}`,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"scrapegoat_crawl","arguments":"not-an-object"}}`,
		// Deep nesting, to probe recursive decoding.
		`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"a":{"a":{"a":{"a":{"a":{}}}}}}}`,
	} {
		f.Add(seed)
	}

	server := NewServer(fuzzLogger(), "")

	f.Fuzz(func(t *testing.T, message string) {
		resp, err := server.HandleMessage(context.Background(), []byte(message))
		if err != nil || resp == nil {
			return // an error or a no-response notification are both fine
		}

		// Anything we do write back must be valid JSON, or we corrupt the stream
		// for a client that is still reading it.
		if !json.Valid(resp) {
			t.Fatalf("emitted invalid JSON for input %q:\n%s", message, resp)
		}
	})
}

// FuzzToolArguments fuzzes the argument payload of a tool call with the tool name
// held fixed, reaching the per-tool unmarshalling that FuzzHandleMessage mostly
// bounces off.
func FuzzToolArguments(f *testing.F) {
	for _, seed := range []string{
		`{"url":"https://example.com"}`,
		`{"url":"https://example.com","max_depth":3,"concurrency":5}`,
		`{"url":"https://example.com","max_depth":-1}`,
		`{"url":"https://example.com","max_depth":999999999}`,
		`{"url":123}`,
		`{"url":null}`,
		`{}`,
		`{"urls":[]}`,
		`{"urls":["https://a.example","https://b.example"]}`,
		`[]`,
		`null`,
		``,
	} {
		f.Add(seed)
	}

	server := NewServer(fuzzLogger(), "")

	f.Fuzz(func(t *testing.T, args string) {
		if !json.Valid([]byte(args)) {
			// Invalid JSON is covered by FuzzHandleMessage; here we want the
			// decoder to reach a tool handler.
			return
		}
		// scrapegoat_job_status is the tool that reaches no network, so it can be
		// driven at fuzzing rates. The fetching tools are covered by
		// FuzzToolURLValidation below and by ssrf_test.go.
		_, _ = server.tools.Execute(context.Background(), "scrapegoat_job_status",
			json.RawMessage(args))
	})
}

// FuzzToolURLValidation fuzzes the check every URL-taking tool runs before it
// touches the network. It is the security-relevant half of tool argument handling,
// and unlike the handlers it is pure, so it can be driven at fuzzing rates.
func FuzzToolURLValidation(f *testing.F) {
	for _, seed := range []string{
		"https://example.com",
		"http://169.254.169.254/latest/meta-data/",
		"file:///etc/passwd",
		"gopher://127.0.0.1:6379/_INFO",
		"http://[::1]:8080/",
		"//protocol-relative",
		"https://",
		"garbage",
		"",
		"https://example.com/\x00",
		"https://user:pass@example.com",
		"HTTPS://EXAMPLE.COM",
	} {
		f.Add(seed)
	}

	registry := NewServer(fuzzLogger(), "").tools

	f.Fuzz(func(t *testing.T, rawURL string) {
		err := registry.checkURL(rawURL)

		// Anything the check accepts must have an http/https scheme and a host.
		// A gap here is an SSRF hole, not a cosmetic bug.
		if err == nil {
			u, perr := url.Parse(rawURL)
			if perr != nil {
				t.Fatalf("accepted a URL that does not parse: %q", rawURL)
			}
			scheme := strings.ToLower(u.Scheme)
			if scheme != "http" && scheme != "https" {
				t.Fatalf("accepted scheme %q from %q", u.Scheme, rawURL)
			}
			if u.Hostname() == "" {
				t.Fatalf("accepted a URL with no host: %q", rawURL)
			}
		}
	})
}

// fuzzLogger discards output; fuzzing a chatty server drowns the corpus report.
func fuzzLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}
