package clientbridge

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/KeepS13/HQIP/internal/client"
)

// Unimplemented methods deliberately panic through the embedded interface: a
// malformed/unknown command must never reach a state-changing controller call.
type stateController struct{ Controller }

func (*stateController) View() client.View {
	return client.View{LoggedIn: true, DeviceName: "test", Status: client.Status{RunState: "connected"}}
}

func TestBridgeRejectsUnknownCommandsAndReturnsSafeState(t *testing.T) {
	bridge := &Bridge{Controller: &stateController{}}
	for _, raw := range []string{`{"id":1,"method":"exec","params":{"path":"cmd.exe"}}`, `{"id":0,"method":"connect"}`, `{`, strings.Repeat("x", 65537)} {
		var reply Response
		if err := json.Unmarshal([]byte(bridge.Call(raw)), &reply); err != nil || reply.Error == "" {
			t.Fatal("unsafe command accepted", err)
		}
	}
	raw := bridge.Call(`{"id":2,"method":"state"}`)
	if strings.Contains(raw, "accessToken") || strings.Contains(raw, "password") || !strings.Contains(raw, `"runState":"connected"`) {
		t.Fatal("state contract changed", raw)
	}
}
