package clientbridge

import (
	"errors"
	"testing"

	"github.com/KeepS13/HQIP/internal/client"
)

type loginReturnController struct {
	Controller
	logged bool
	err    error
}

func (c *loginReturnController) View() client.View                             { return client.View{LoggedIn: c.logged} }
func (c *loginReturnController) LoginBrowser(string, func(string) error) error { return c.err }

func TestBrowserLoginReturnsToPreviewOnlyAfterCredentialsSaved(t *testing.T) {
	for _, tc := range []struct {
		name   string
		logged bool
		err    error
		want   int
	}{
		{"success", true, nil, 1},
		{"initial sync unavailable", true, errors.New("sync unavailable"), 1},
		{"cancelled", false, errors.New("cancelled"), 0},
		{"network unavailable", false, errors.New("timeout"), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			count := 0
			bridge := &Bridge{Controller: &loginReturnController{logged: tc.logged, err: tc.err}, LoginComplete: func() { count++ }}
			_, err := bridge.dispatch(Request{Method: "login"})
			if count != tc.want || !errors.Is(err, tc.err) {
				t.Fatalf("return count=%d, error=%v", count, err)
			}
			// Legacy hosts omit the hook; they retain their existing behavior.
			bridge.LoginComplete = nil
			if _, err = bridge.dispatch(Request{Method: "login"}); !errors.Is(err, tc.err) {
				t.Fatal(err)
			}
		})
	}
}
