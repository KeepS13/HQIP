package clientbridge

import (
	"encoding/json"
	"testing"

	"github.com/KeepS13/HQIP/internal/client"
)

type probeController struct {
	Controller
	method, subscription, node string
	server                     string
}

func (c *probeController) View() client.View { return client.View{ServerURL: c.server} }
func (c *probeController) TestSelectedNodeDelay(sub, node string) error {
	c.method, c.subscription, c.node = "single", sub, node
	return nil
}
func (c *probeController) CancelSelectedNodeDelay(sub, node string) {
	c.method, c.subscription, c.node = "cancel-single", sub, node
}
func (c *probeController) TestNodeDelays() error { c.method = "all"; return nil }
func (c *probeController) CancelNodeDelays()     { c.method = "cancel-all" }

func TestBridgeSeparatesSingleAndCatalogProbe(t *testing.T) {
	c := &probeController{}
	b := Bridge{Controller: c}
	for command, want := range map[string]string{"testNode": "single", "cancelNodeTest": "cancel-single", "testNodes": "all", "cancelTest": "cancel-all"} {
		var response Response
		if err := json.Unmarshal([]byte(b.Call(`{"id":1,"method":"`+command+`","params":{"subscriptionId":"sub","nodeId":"node"}}`)), &response); err != nil {
			t.Fatal(err)
		}
		if response.Error != "" || c.method != want {
			t.Fatal(response, c.method)
		}
		if (command == "testNode" || command == "cancelNodeTest") && (c.subscription != "sub" || c.node != "node") {
			t.Fatal("target lost")
		}
	}
}

func TestShopUsesAccountOriginAndCatalogRoute(t *testing.T) {
	for server, want := range map[string]string{client.DefaultServerURL: client.UserSiteURL + "/catalog", "https://custom.example/": "https://custom.example/catalog"} {
		c := &probeController{server: server}
		opened := ""
		b := Bridge{Controller: c, OpenURL: func(url string) error { opened = url; return nil }}
		var response Response
		_ = json.Unmarshal([]byte(b.Call(`{"id":1,"method":"shop","params":{"server":"https://ignored.example"}}`)), &response)
		if response.Error != "" || opened != want {
			t.Fatal(response, opened)
		}
	}
}
