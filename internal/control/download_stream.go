package control

import (
	"errors"
	"io"
	"net/http"
	"time"
)

// downloadWriter retains the ordinary 30-second stalled-writer bound, renewing
// it after each successful chunk instead of imposing a 30-second total transfer
// limit. Hiding ReaderFrom makes ServeContent use bounded chunks, so sendfile
// cannot bypass deadline renewal on a slow EXE/APK download.
type downloadWriter struct{ http.ResponseWriter }

func (w downloadWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w downloadWriter) Write(data []byte) (int, error) {
	err := http.NewResponseController(w.ResponseWriter).SetWriteDeadline(time.Now().Add(30 * time.Second))
	// In-memory recorders have no connection deadline; real net/http writers do.
	if err != nil && !errors.Is(err, http.ErrNotSupported) {
		return 0, err
	}
	return w.ResponseWriter.Write(data)
}

// serveVerifiedDownload serves an already authenticated, rewound handle. The
// caller owns its lifetime and must close it after this function returns.
func serveVerifiedDownload(w http.ResponseWriter, r *http.Request, name string, modified time.Time, source io.ReadSeeker) {
	http.ServeContent(downloadWriter{w}, r, name, modified, source)
}
