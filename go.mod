module github.com/KeepS13/HQIP

go 1.26.10

require (
	github.com/Microsoft/go-winio v0.6.4
	github.com/KeepS13/HQIP/runtime/nodepolicy v0.0.0
	github.com/alicebob/miniredis/v2 v2.40.0
	github.com/apernet/hysteria/core/v2 v2.0.0
	github.com/apernet/quic-go v0.63.1-0.20261004180939-a10df75c260c
	github.com/jackc/pgx/v5 v5.12.0
	github.com/metacubex/utls v1.8.7
	github.com/redis/go-redis/v9 v9.23.0
	github.com/sagernet/sing v0.9.7-0.20261023115459-5dc452e537bf
	github.com/sagernet/sing-vmess v0.2.9-0.20261019223527-a0c58542bd9d
	golang.org/x/crypto v0.58.0
	golang.org/x/net v0.62.0
	golang.org/x/sys v0.49.0
	golang.org/x/text v0.43.0
	gopkg.in/yaml.v3 v3.0.1
	modernc.org/sqlite v1.61.1
)

// HY2 and Reality consume the same independent nodepolicy business module.
replace github.com/apernet/hysteria/core/v2 => ./runtime/hysteria/core

require (
	github.com/andybalholm/brotli v1.2.7 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/gofrs/uuid/v5 v5.3.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.3 // indirect
	github.com/klauspost/compress v1.20.2 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/quic-go/qpack v0.6.0 // indirect
	github.com/refraction-networking/utls v1.8.2 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/rogpeppe/go-internal v1.16.0 // indirect
	github.com/stretchr/objx v0.5.3 // indirect
	github.com/stretchr/testify v1.12.1 // indirect
	github.com/yuin/gopher-lua v1.1.1 // indirect
	go.uber.org/atomic v1.12.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/exp v0.0.0-20240904232852-e7e105dedf7e // indirect
	golang.org/x/sync v0.24.0 // indirect
	modernc.org/libc v1.79.0 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

replace github.com/KeepS13/HQIP/runtime/nodepolicy => ./runtime/nodepolicy
