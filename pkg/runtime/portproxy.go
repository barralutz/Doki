package runtime

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
)

type tcpForward struct {
	listener net.Listener
	target   string

	once  sync.Once
	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

func startTCPForward(spec PortForward) (io.Closer, error) {
	if spec.ListenPort == 0 || spec.TargetPort == 0 || spec.TargetHost == "" {
		return nil, fmt.Errorf("invalid TCP forward: listen port, target host, and target port are required")
	}
	listenHost := spec.ListenHost
	if listenHost == "" {
		listenHost = "0.0.0.0"
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(listenHost, strconv.Itoa(int(spec.ListenPort))))
	if err != nil {
		return nil, fmt.Errorf("listen TCP forward %s:%d: %w", listenHost, spec.ListenPort, err)
	}
	f := &tcpForward{
		listener: ln,
		target:   net.JoinHostPort(spec.TargetHost, strconv.Itoa(int(spec.TargetPort))),
		conns:    make(map[net.Conn]struct{}),
	}
	go f.acceptLoop()
	return f, nil
}

func (f *tcpForward) acceptLoop() {
	for {
		client, err := f.listener.Accept()
		if err != nil {
			return
		}
		go f.handle(client)
	}
}

func (f *tcpForward) handle(client net.Conn) {
	target, err := net.Dial("tcp", f.target)
	if err != nil {
		_ = client.Close()
		return
	}
	f.track(client, target)
	defer func() {
		_ = client.Close()
		_ = target.Close()
		f.untrack(client, target)
	}()

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(target, client); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, target); done <- struct{}{} }()
	<-done
}

func (f *tcpForward) track(conns ...net.Conn) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, conn := range conns {
		f.conns[conn] = struct{}{}
	}
}

func (f *tcpForward) untrack(conns ...net.Conn) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, conn := range conns {
		delete(f.conns, conn)
	}
}

func (f *tcpForward) Close() error {
	var err error
	f.once.Do(func() {
		err = f.listener.Close()
		f.mu.Lock()
		for conn := range f.conns {
			_ = conn.Close()
		}
		f.conns = make(map[net.Conn]struct{})
		f.mu.Unlock()
	})
	if ne, ok := err.(*net.OpError); ok && ne.Err != nil {
		// Closing a listener is expected to unblock Accept; net.Listener.Close
		// itself normally returns nil, but keep a real close failure visible.
		return ne
	}
	return err
}

func (rt *Runtime) replacePortProxies(containerID string, specs []PortForward) error {
	rt.closePortProxies(containerID)
	if len(specs) == 0 {
		return nil
	}
	started := make([]io.Closer, 0, len(specs))
	for _, spec := range specs {
		forward, err := startTCPForward(spec)
		if err != nil {
			for _, closer := range started {
				_ = closer.Close()
			}
			return err
		}
		started = append(started, forward)
	}
	rt.portProxyMu.Lock()
	if rt.portProxies == nil {
		rt.portProxies = make(map[string][]io.Closer)
	}
	rt.portProxies[containerID] = started
	rt.portProxyMu.Unlock()
	return nil
}

func (rt *Runtime) closePortProxies(containerID string) {
	rt.portProxyMu.Lock()
	proxies := rt.portProxies[containerID]
	delete(rt.portProxies, containerID)
	rt.portProxyMu.Unlock()
	for _, proxy := range proxies {
		_ = proxy.Close()
	}
}
