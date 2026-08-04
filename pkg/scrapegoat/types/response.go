package types

import (
	"io"
	"net/http"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// Response represents the result of fetching a request.
type Response struct {
	// StatusCode is the HTTP status code.
	StatusCode int

	// Headers are the response HTTP headers.
	Headers http.Header

	// Body is the raw response body bytes.
	Body []byte

	// Request is a reference to the original request.
	Request *Request

	// ContentType is the MIME type of the response.
	ContentType string

	// ContentLength is the size of the response body in bytes.
	ContentLength int64

	// FinalURL is the URL after any redirects.
	FinalURL string

	// Doc is a parsed goquery document (lazily loaded).
	Doc *goquery.Document

	// FetchDuration is how long the fetch took.
	FetchDuration time.Duration

	// FetchedAt is when this response was received.
	FetchedAt time.Time

	// Meta stores arbitrary metadata.
	Meta map[string]any
}

// NewResponse creates a Response from an http.Response.
func NewResponse(req *Request, httpResp *http.Response, body []byte, duration time.Duration) *Response {
	resp := &Response{
		StatusCode:    httpResp.StatusCode,
		Headers:       httpResp.Header,
		Body:          body,
		Request:       req,
		ContentType:   httpResp.Header.Get("Content-Type"),
		ContentLength: int64(len(body)),
		FinalURL:      httpResp.Request.URL.String(),
		FetchDuration: duration,
		FetchedAt:     time.Now(),
		Meta:          make(map[string]any),
	}
	return resp
}

// NewBrowserResponse creates a Response from headless browser output.
func NewBrowserResponse(req *Request, statusCode int, body []byte, finalURL string, duration time.Duration) *Response {
	return &Response{
		StatusCode:    statusCode,
		Headers:       make(http.Header),
		Body:          body,
		Request:       req,
		ContentType:   "text/html",
		ContentLength: int64(len(body)),
		FinalURL:      finalURL,
		FetchDuration: duration,
		FetchedAt:     time.Now(),
		Meta:          make(map[string]any),
	}
}

// Document returns a parsed goquery document, lazily initializing it.
func (r *Response) Document() (*goquery.Document, error) {
	if r.Doc != nil {
		return r.Doc, nil
	}
	doc, err := goquery.NewDocumentFromReader(io.NopCloser(
		io.LimitReader(
			&bytesReader{data: r.Body, pos: 0},
			int64(len(r.Body)),
		),
	))
	if err != nil {
		return nil, err
	}
	r.Doc = doc
	return doc, nil
}

// IsSuccess returns true if the response status is 2xx.
func (r *Response) IsSuccess() bool {
	return r.StatusCode >= 200 && r.StatusCode < 300
}

// IsRedirect returns true if the response status is 3xx.
func (r *Response) IsRedirect() bool {
	return r.StatusCode >= 300 && r.StatusCode < 400
}

// IsClientError returns true if the response status is 4xx.
func (r *Response) IsClientError() bool {
	return r.StatusCode >= 400 && r.StatusCode < 500
}

// IsServerError returns true if the response status is 5xx.
func (r *Response) IsServerError() bool {
	return r.StatusCode >= 500 && r.StatusCode < 600
}

// bytesReader implements io.Reader for a byte slice.
type bytesReader struct {
	data []byte
	pos  int
}

func (r *bytesReader) Read(p []byte) (n int, err error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n = copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}
