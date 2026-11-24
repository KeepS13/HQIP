package control

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type slowDownloadSource struct{ *bytes.Reader }

func (source slowDownloadSource) Read(data []byte) (int, error) {
	time.Sleep(35 * time.Millisecond)
	if len(data) > 16 {
		data = data[:16]
	}
	return source.Reader.Read(data)
}

func TestVerifiedDownloadOutlivesOrdinaryWriteTimeout(t *testing.T) {
	payload := bytes.Repeat([]byte("verified download"), 16)
	for _, ranged := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "range"}[ranged], func(t *testing.T) {
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/octet-stream")
				serveVerifiedDownload(w, r, "package.apk", time.Time{}, slowDownloadSource{bytes.NewReader(payload)})
			}))
			server.Config.WriteTimeout = 100 * time.Millisecond
			server.Start()
			defer server.Close()
			request, _ := http.NewRequest("GET", server.URL, nil)
			expected, status := payload, http.StatusOK
			if ranged {
				request.Header.Set("Range", "bytes=16-175")
				expected, status = payload[16:176], http.StatusPartialContent
			}
			client := server.Client()
			client.Timeout = 5 * time.Second
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			data, err := io.ReadAll(response.Body)
			if err != nil || response.StatusCode != status || !bytes.Equal(data, expected) {
				t.Fatalf("truncated download: status=%d size=%d err=%v", response.StatusCode, len(data), err)
			}
		})
	}
}
