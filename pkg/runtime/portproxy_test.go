package runtime

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

func freeTCPPort(t *testing.T) uint16 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return uint16(ln.Addr().(*net.TCPAddr).Port)
}

func startEchoServer(t *testing.T) (string, io.Closer) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()
	return ln.Addr().String(), ln
}

func TestTCPForwardRoundTripsAndCloseReleasesListener(t *testing.T) {
	target, echo := startEchoServer(t)
	defer echo.Close()
	host, portText, _ := net.SplitHostPort(target)
	targetPort, _ := strconv.Atoi(portText)
	listenPort := freeTCPPort(t)

	forward, err := startTCPForward(PortForward{
		ListenHost: "127.0.0.1", ListenPort: listenPort,
		TargetHost: host, TargetPort: uint16(targetPort),
	})
	if err != nil {
		t.Fatal(err)
	}

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", listenPort), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := conn.Write([]byte("provider-port-proxy")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len("provider-port-proxy"))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if string(buf) != "provider-port-proxy" {
		t.Fatalf("round trip=%q", buf)
	}

	if err := forward.Close(); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", listenPort))
	if err != nil {
		t.Fatalf("listener not released after Close: %v", err)
	}
	_ = ln.Close()
}

func TestTCPForwardRejectsInvalidSpecAndOccupiedPort(t *testing.T) {
	if _, err := startTCPForward(PortForward{}); err == nil {
		t.Fatal("empty forwarding spec unexpectedly accepted")
	}
	port := freeTCPPort(t)
	occupied, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	if _, err := startTCPForward(PortForward{ListenHost: "127.0.0.1", ListenPort: port, TargetHost: "127.0.0.1", TargetPort: 5432}); err == nil {
		t.Fatal("occupied listen port unexpectedly accepted")
	}
}
