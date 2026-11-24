package clientbridge

import (
	"encoding/json"
	"testing"

	"github.com/KeepS13/HQIP/internal/client"
)

type choiceController struct {
	Controller
	view client.View
}

func (c *choiceController) SelectSubscriptionNode(subscription, node string) error {
	c.view.DesiredSubscriptionID, c.view.DesiredNodeID = subscription, node
	return nil
}
func (c *choiceController) SelectSubscription(subscription string) error {
	c.view.DesiredSubscriptionID = subscription
	return nil
}
func (c *choiceController) View() client.View { return c.view }

// The command response is the persisted presentation snapshot, allowing Godot
// to display a local choice without a second state request or connection state.
func TestSelectionReplyIncludesConfirmedView(t *testing.T) {
	controller := &choiceController{view: client.View{Status: client.Status{RunState: "disconnected"}}}
	bridge := &Bridge{Controller: controller}
	for _, method := range []string{"node", "subscription"} {
		raw := bridge.Call(`{"id":1,"method":"` + method + `","params":{"subscriptionId":"b","nodeId":"node-b"}}`)
		var reply struct {
			Result client.View
			Error  string
		}
		if err := json.Unmarshal([]byte(raw), &reply); err != nil {
			t.Fatal(err)
		}
		if reply.Error != "" || reply.Result.DesiredSubscriptionID != "b" || reply.Result.SelectionPending || reply.Result.ConnectionIntent {
			t.Fatal("selection reply is incomplete or implies connection", raw)
		}
		if method == "node" && reply.Result.DesiredNodeID != "node-b" {
			t.Fatal("node missing from acknowledgement")
		}
	}
}
