package client

import (
	"io"
	"net/http"
	"strconv"
	"strings"
)

type previewProgressTransport struct {
	base    http.RoundTripper
	updater *FlutterUpdater
}

func (t previewProgressTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err == nil && strings.HasPrefix(req.URL.Path, "/downloads/flutter/preview/") {
		offset := int64(0)
		if resp.StatusCode == http.StatusPartialContent {
			offset, _ = strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(req.Header.Get("Range"), "bytes="), "-"), 10, 64)
		}
		t.updater.mu.Lock()
		t.updater.view.Downloaded = offset
		t.updater.mu.Unlock()
		resp.Body = &previewProgressReader{ReadCloser: resp.Body, updater: t.updater}
	}
	return resp, err
}
func (t previewProgressTransport) CloseIdleConnections() {
	if c, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

type previewProgressReader struct {
	io.ReadCloser
	updater *FlutterUpdater
}

func (r *previewProgressReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.updater.mu.Lock()
	r.updater.view.Downloaded += int64(n)
	r.updater.mu.Unlock()
	return n, err
}
