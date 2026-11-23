package reality

import (
	"context"
	"github.com/KeepS13/HQIP/internal/outbound"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"io"
	"net"
)

type forwarder struct {
	backend outbound.Client
	failed  func(error)
}

// Both copy directions must finish before the ingress group reference is
// released. Closing either side interrupts the other direction immediately.
func (f *forwarder) NewConnectionEx(ctx context.Context, conn net.Conn, source, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	defer conn.Close()
	remote, err := f.backend.TCP(destination.String())
	if err != nil {
		f.failed(err)
	}
	if err == nil {
		defer remote.Close()
		stop := context.AfterFunc(ctx, func() { _ = conn.Close(); _ = remote.Close() })
		defer stop()
		done := make(chan struct{})
		go func() { _, _ = io.Copy(remote, conn); _ = remote.Close(); _ = conn.Close(); close(done) }()
		_, err = io.Copy(conn, remote)
		_ = conn.Close()
		_ = remote.Close()
		<-done
	}
	if onClose != nil {
		onClose(err)
	}
}

func (f *forwarder) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, source, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	defer conn.Close()
	remote, err := f.backend.UDP()
	if err != nil {
		f.failed(err)
	}
	if err == nil {
		defer remote.Close()
		stop := context.AfterFunc(ctx, func() { _ = conn.Close(); _ = remote.Close() })
		defer stop()
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer conn.Close()
			defer remote.Close()
			buffer := buf.NewSize(65535)
			defer buffer.Release()
			for {
				buffer.Reset()
				dest, e := conn.ReadPacket(buffer)
				if e != nil || remote.Send(buffer.Bytes(), dest.String()) != nil {
					return
				}
			}
		}()
		for {
			var data []byte
			var addr string
			data, addr, err = remote.Receive()
			if err != nil {
				break
			}
			// Packet writers consume the buffer, including required VLESS/mux
			// framing headroom. A slice wrapper would panic in non-mux UDP.
			headroom := N.CalculateFrontHeadroom(conn)
			packet := buf.NewSize(headroom + len(data))
			packet.Advance(headroom)
			_, _ = packet.Write(data)
			if err = conn.WritePacket(packet, M.ParseSocksaddr(addr)); err != nil {
				break
			}
		}
		_ = conn.Close()
		_ = remote.Close()
		<-done
	}
	if onClose != nil {
		onClose(err)
	}
}
