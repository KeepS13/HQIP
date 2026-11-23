module github.com/KeepS13/HQIP/desktop

go 1.26.10

require (
	github.com/KeepS13/HQIP/runtime/nodepolicy v0.0.0 // indirect
	github.com/KeepS13/HQIP v0.0.0
	github.com/wailsapp/wails/v3 v3.1.0
	golang.org/x/sys v0.49.0
)

require (
	github.com/andybalholm/brotli v1.2.7 // indirect
	github.com/apernet/hysteria/core/v2 v2.0.0 // indirect
	github.com/apernet/quic-go v0.63.1-0.20261004180939-a10df75c260c // indirect
	github.com/coder/websocket v1.8.14 // indirect
	github.com/ebitengine/purego v0.11.1 // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/gofrs/uuid/v5 v5.3.2 // indirect
	github.com/klauspost/compress v1.20.2 // indirect
	github.com/metacubex/utls v1.8.7 // indirect
	github.com/quic-go/qpack v0.6.0 // indirect
	github.com/refraction-networking/utls v1.8.2 // indirect
	github.com/sagernet/sing v0.9.7-0.20261023115459-5dc452e537bf // indirect
	github.com/sagernet/sing-vmess v0.2.9-0.20261019223527-a0c58542bd9d // indirect
	github.com/stretchr/objx v0.5.3 // indirect
	github.com/stretchr/testify v1.12.1 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/exp v0.0.0-20260410095643-746e56fc9e2f // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/term v0.47.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)

replace github.com/KeepS13/HQIP => ..

replace github.com/apernet/hysteria/core/v2 => ../runtime/hysteria/core

replace github.com/KeepS13/HQIP/runtime/nodepolicy => ../runtime/nodepolicy
