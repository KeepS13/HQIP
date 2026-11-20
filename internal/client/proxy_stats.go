package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"golang.org/x/net/http2"
	"io"
	"math"
	"net"
	"net/http"
)

func readProxyStats(ctx context.Context, port int) (int64, int64, error) {
	transport := &http2.Transport{AllowHTTP: true, DialTLSContext: func(ctx context.Context, network, address string, _ *tls.Config) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:"+portString(port)+"/v2ray.core.app.stats.command.StatsService/QueryStats", bytes.NewReader([]byte{0, 0, 0, 0, 0}))
	if err != nil {
		return 0, 0, err
	}
	request.Header.Set("Content-Type", "application/grpc")
	request.Header.Set("TE", "trailers")
	response, err := transport.RoundTrip(request)
	if err != nil {
		return 0, 0, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return 0, 0, err
	}
	if response.StatusCode != 200 || response.Trailer.Get("Grpc-Status") != "0" || len(raw) < 5 || raw[0] != 0 || int(binary.BigEndian.Uint32(raw[1:5])) != len(raw)-5 {
		return 0, 0, errors.New("代理出口统计暂不可用")
	}
	return decodeProxyStats(raw[5:])
}

func protobufField(raw []byte) (uint64, []byte, uint64, []byte, error) {
	key, count := binary.Uvarint(raw)
	if count <= 0 {
		return 0, nil, 0, nil, errors.New("invalid stats field")
	}
	raw = raw[count:]
	switch key & 7 {
	case 0:
		value, size := binary.Uvarint(raw)
		if size <= 0 {
			return 0, nil, 0, nil, errors.New("invalid stats integer")
		}
		return key >> 3, nil, value, raw[size:], nil
	case 2:
		size, count := binary.Uvarint(raw)
		if count <= 0 || size > uint64(len(raw)-count) {
			return 0, nil, 0, nil, errors.New("invalid stats length")
		}
		return key >> 3, raw[count : count+int(size)], 0, raw[count+int(size):], nil
	case 1:
		if len(raw) >= 8 {
			return key >> 3, nil, 0, raw[8:], nil
		}
	case 5:
		if len(raw) >= 4 {
			return key >> 3, nil, 0, raw[4:], nil
		}
	}
	return 0, nil, 0, nil, errors.New("invalid stats encoding")
}

func decodeProxyStats(raw []byte) (int64, int64, error) {
	var upload, download int64
	seen := map[string]bool{}
	for len(raw) > 0 {
		field, entry, _, rest, err := protobufField(raw)
		if err != nil {
			return 0, 0, err
		}
		raw = rest
		if field != 1 {
			continue
		}
		var name string
		var value uint64
		for len(entry) > 0 {
			field, data, number, rest, err := protobufField(entry)
			if err != nil {
				return 0, 0, err
			}
			entry = rest
			if field == 1 {
				name = string(data)
			}
			if field == 2 {
				value = number
			}
		}
		if name != "outbound>>>proxy>>>traffic>>>uplink" && name != "outbound>>>proxy>>>traffic>>>downlink" {
			continue
		}
		if value > math.MaxInt64 || seen[name] {
			return 0, 0, errors.New("invalid proxy counter")
		}
		seen[name] = true
		if name == "outbound>>>proxy>>>traffic>>>uplink" {
			upload = int64(value)
		} else {
			download = int64(value)
		}
	}
	return upload, download, nil
}
