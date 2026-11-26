package previewhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func Identity() (string, string, error) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", "", err
	}
	sid := user.User.Sid.String()
	sum := sha256.Sum256([]byte(sid))
	return sid, hex.EncodeToString(sum[:12]), nil
}
func PipeName() (string, error) {
	_, id, err := Identity()
	return `\\.\pipe\HQIP.Flutter.Preview.` + id, err
}
func Listen() (net.Listener, error) {
	sid, _, err := Identity()
	if err != nil {
		return nil, err
	}
	name, _ := PipeName()
	return winio.ListenPipe(name, &winio.PipeConfig{SecurityDescriptor: "D:P(A;;GA;;;" + sid + ")", InputBufferSize: 65536, OutputBufferSize: 65536})
}
func Dial(ctx context.Context) (net.Conn, error) {
	name, err := PipeName()
	if err != nil {
		return nil, err
	}
	return winio.DialPipeContext(ctx, name)
}
