package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHQIPAdmissionRejectsTruncatedHTTPResponse(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprintf("complete=%t", complete), func(t *testing.T) {
			local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, writer, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.Close()
				body := `{"leaseId":"grant"}`
				_, _ = fmt.Fprintf(writer, "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n", len(body), body)
				if complete {
					_, _ = writer.WriteString("0\r\n\r\n")
				}
				_ = writer.Flush()
			}))
			defer local.Close()
			a, err := NewAdmission(AdmissionConfig{URL: local.URL, Secret: admissionTestSecret})
			if err != nil {
				t.Fatal(err)
			}
			grant, err := a.request(context.Background(), "/admission", struct{}{})
			if complete && (err != nil || grant.LeaseID != "grant") {
				t.Fatalf("complete response rejected: %+v %v", grant, err)
			}
			if !complete && err == nil {
				t.Fatal("accepted JSON from an incomplete HTTP response")
			}
		})
	}
}
