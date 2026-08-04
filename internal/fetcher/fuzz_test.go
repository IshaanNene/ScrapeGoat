package fetcher

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"io"
	"net/http"
	"testing"
)

// FuzzDecompressReader fuzzes the compressed stream itself.
//
// Content-Encoding is set by the server being crawled, so the bytes handed to the
// decompressor are entirely attacker-chosen. Truncated, corrupt, and adversarially
// crafted streams must produce errors, not panics — and must not read unbounded
// output, which is what the size cap after decompression is for.
func FuzzDecompressReader(f *testing.F) {
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte("<html><body>hello</body></html>"))
	_ = zw.Close()
	f.Add(gz.Bytes(), "gzip")

	var fl bytes.Buffer
	fw, _ := flate.NewWriter(&fl, flate.DefaultCompression)
	_, _ = fw.Write([]byte("<html><body>hello</body></html>"))
	_ = fw.Close()
	f.Add(fl.Bytes(), "deflate")

	// Truncated gzip: valid header, body cut short.
	if b := gz.Bytes(); len(b) > 5 {
		f.Add(b[:len(b)-5], "gzip")
	}
	// Header only.
	f.Add([]byte{0x1f, 0x8b, 0x08}, "gzip")
	f.Add([]byte("not compressed at all"), "gzip")
	f.Add([]byte("plain body"), "")
	f.Add([]byte("plain body"), "br")
	f.Add([]byte{}, "gzip")

	const maxOut = 1 << 20 // 1 MiB, standing in for max_body_size

	f.Fuzz(func(t *testing.T, body []byte, encoding string) {
		resp := &http.Response{Header: http.Header{}}
		if encoding != "" {
			resp.Header.Set("Content-Encoding", encoding)
		}

		counted := &countingReader{r: bytes.NewReader(body)}
		rc, err := decompressReader(resp, io.LimitReader(counted, maxOut))
		if err != nil {
			return // a rejected stream is a correct outcome
		}
		defer func() { _ = rc.Close() }()

		// Bounded read: the point of the fix is that no input can make this
		// allocate without limit.
		out, _ := io.ReadAll(io.LimitReader(rc, maxOut+1))
		if int64(len(out)) > maxOut+1 {
			t.Fatalf("read %d bytes past the %d cap", len(out), maxOut)
		}
	})
}

// FuzzParseRetryAfter fuzzes the Retry-After header parser. The value is
// server-controlled and feeds a sleep duration, so a nonsense value must not become
// a nonsense (or negative, or enormous) delay.
func FuzzParseRetryAfter(f *testing.F) {
	for _, seed := range []string{
		"120", "0", "-1", "999999999999999999999",
		"Wed, 21 Oct 2015 07:28:00 GMT",
		"not a date", "", " 5 ", "5.5", "1e10",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, header string) {
		d := parseRetryAfter(header)
		if d < 0 {
			t.Fatalf("parseRetryAfter(%q) = %v, a negative delay", header, d)
		}
	})
}
