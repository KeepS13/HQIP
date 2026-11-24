package mobile

import (
	"encoding/json"
	"slices"
	"sync"
)

// networkChanges owns the last physical egress snapshot. Android can deliver
// duplicate events or events for an unused cellular network while Wi-Fi stays
// healthy. Only an actual selected-network change may reset active sockets.
// The callback runs under mu, so resets cannot overtake newer network updates.
type networkChanges struct {
	mu          sync.Mutex
	key         string
	initialized bool
}

func (n *networkChanges) update(raw string, apply func()) error {
	key, err := selectedNetworkKey(raw)
	if err != nil {
		return err // A failed snapshot is not evidence that the network was lost.
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.initialized && n.key == key {
		return nil
	}
	n.key, n.initialized = key, true
	apply()
	return nil
}

func selectedNetworkKey(raw string) (string, error) {
	var networks []struct {
		ID        int64    `json:"networkId"`
		Name      string   `json:"name"`
		Index     int32    `json:"index"`
		MTU       int32    `json:"mtu"`
		Addresses []string `json:"addresses"`
		DNS       []string `json:"dns"`
		Default   bool     `json:"default"`
	}
	if err := json.Unmarshal([]byte(raw), &networks); err != nil {
		return "", err
	}
	for _, network := range networks {
		if network.Default {
			// Address enumeration order is not a route change. DNS priority is.
			slices.Sort(network.Addresses)
			key, err := json.Marshal(network)
			return string(key), err
		}
	}
	return "offline", nil
}
