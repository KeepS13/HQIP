package client

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// transferBundle owns retries within one signed download and its shared deadline.
// Completed bytes survive transient disconnects; no retry can change the URL or
// signed size. The caller closes, hashes and publishes the file only on success.
func (u *BundleUpdater) transferBundle(ctx context.Context, url string, file *os.File, size int64) error {
	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(time.Duration(attempt) * 250 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		offset, err := file.Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept-Encoding", "identity")
		if offset > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		}
		response, err := u.http.Do(req)
		if err != nil {
			var certificateError *tls.CertificateVerificationError
			if errors.As(err, &certificateError) {
				return err
			}
			lastErr = err
			continue
		}
		retry, err := appendBundleResponse(file, response, offset, size)
		response.Body.Close()
		if !retry {
			return err
		}
		lastErr = err
	}
	return lastErr
}

// appendBundleResponse never appends an unacknowledged range. A server that
// ignores Range may send a full 200 response, which replaces the partial file.
// Only transport truncation is retryable; protocol and disk errors fail closed.
func appendBundleResponse(file *os.File, response *http.Response, offset, size int64) (bool, error) {
	if encoding := response.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		return false, errors.New("encoded update range")
	}
	switch response.StatusCode {
	case http.StatusOK:
		if offset > 0 {
			if err := file.Truncate(0); err != nil {
				return false, err
			}
			if _, err := file.Seek(0, io.SeekStart); err != nil {
				return false, err
			}
			offset = 0
		}
	case http.StatusPartialContent:
		if response.Header.Get("Content-Range") != fmt.Sprintf("bytes %d-%d/%d", offset, size-1, size) {
			return false, errors.New("invalid update range")
		}
	default:
		return false, errors.New("update download unavailable")
	}
	remaining := size - offset
	if response.ContentLength >= 0 && response.ContentLength != remaining {
		return false, errors.New("invalid update content length")
	}
	n, err := io.Copy(file, io.LimitReader(response.Body, remaining+1))
	if n > remaining {
		return false, errors.New("oversized update download")
	}
	var diskError *os.PathError
	if errors.As(err, &diskError) {
		return false, err
	}
	if n == remaining {
		return false, nil // The caller still checks the complete authenticated hash.
	}
	if err == nil {
		err = io.ErrUnexpectedEOF
	}
	return true, err
}
