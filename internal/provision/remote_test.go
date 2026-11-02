package provision

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// Regression: ssh.Session copies stdout and stderr concurrently using io.Copy.
// Sharing one *bytes.Buffer invokes ReadFrom twice; an empty stderr reader that
// observes EOF late can reset the buffer length and silently erase stdout.
func TestRemoteRunKeepsStdoutWithConcurrentEmptyStderr(t *testing.T) {
	const output = "ubuntu\n22.04\nx86_64\ncc9c485dcada4ab0a5f4567f718e22e5\n3178280\n"
	client := remoteTestClient(t, func(channel ssh.Channel, command string) {
		// Let the client's two stream-copy workers enter their blocking reads.
		time.Sleep(3 * time.Millisecond)
		if _, err := io.WriteString(channel, output); err != nil {
			t.Errorf("write mock stdout: %v", err)
			return
		}
		// Stdout has data; stderr remains empty until channel EOF is delivered.
		time.Sleep(3 * time.Millisecond)
		_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
	})
	r := &remote{client: client}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for attempt := 0; attempt < 100; attempt++ {
		got, err := r.run(ctx, "preflight", nil)
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if got != output || len(strings.Fields(got)) != 5 {
			t.Fatalf("attempt %d lost stdout: got %q, want %q", attempt, got, output)
		}
	}
}

func TestRemoteRunParsesOnlyStdoutAndPreservesFailureDiagnostic(t *testing.T) {
	client := remoteTestClient(t, func(channel ssh.Channel, command string) {
		_, _ = io.WriteString(channel, "stdout-marker\n")
		_, _ = io.WriteString(channel.Stderr(), "stderr-marker\n")
		status := uint32(0)
		if strings.Contains(command, "failure") {
			status = 7
		}
		_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
	})
	r := &remote{client: client}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := r.run(ctx, "success", nil)
	if err != nil || got != "stdout-marker\n" {
		t.Fatalf("successful command must return parseable stdout only: %q, %v", got, err)
	}
	got, err = r.run(ctx, "failure", nil)
	if err == nil || !strings.Contains(got, "stdout-marker") || !strings.Contains(got, "stderr-marker") {
		t.Fatalf("failed command must preserve stderr diagnostics: %q, %v", got, err)
	}
}

func remoteTestClient(t *testing.T, execute func(ssh.Channel, string)) *ssh.Client {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		server, channels, requests, err := ssh.NewServerConn(conn, config)
		if err != nil {
			t.Errorf("mock SSH handshake: %v", err)
			return
		}
		defer server.Close()
		go ssh.DiscardRequests(requests)
		var workers sync.WaitGroup
		defer workers.Wait()
		for incoming := range channels {
			if incoming.ChannelType() != "session" {
				_ = incoming.Reject(ssh.UnknownChannelType, "sessions only")
				continue
			}
			channel, reqs, err := incoming.Accept()
			if err != nil {
				t.Errorf("accept mock SSH session: %v", err)
				continue
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer channel.Close()
				for request := range reqs {
					if request.Type != "exec" {
						_ = request.Reply(false, nil)
						continue
					}
					var command struct{ Command string }
					if err := ssh.Unmarshal(request.Payload, &command); err != nil {
						t.Errorf("decode mock exec: %v", err)
						_ = request.Reply(false, nil)
						return
					}
					_ = request.Reply(true, nil)
					execute(channel, command.Command)
					_ = channel.CloseWrite()
					return
				}
			}()
		}
	}()
	client, err := ssh.Dial("tcp", listener.Addr().String(), &ssh.ClientConfig{
		User: "root", Timeout: 5 * time.Second,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if !bytes.Equal(key.Marshal(), signer.PublicKey().Marshal()) {
				return fmt.Errorf("unexpected mock SSH host key")
			}
			return nil
		},
	})
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = listener.Close()
		select {
		case <-serverDone:
		case <-time.After(3 * time.Second):
			t.Error("mock SSH server did not stop")
		}
	})
	return client
}
