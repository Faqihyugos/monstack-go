package harness

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TCPConnRecord records an incoming TCP connection and any received data.
type TCPConnRecord struct {
	RemoteAddr      string
	ReceivedPayload []byte
	Timestamp       time.Time
}

// MockTCPServer provides a configurable TCP mock server for E2E TCP prober tests.
type MockTCPServer struct {
	listener       net.Listener
	addr           string
	port           int
	mu             sync.RWMutex
	connections    []TCPConnRecord
	connCh         chan struct{}
	banner         string
	echoMode       bool
	expectMatches  map[string]string // pattern -> response
	immediateClose bool
	hangMode       bool
	delay          time.Duration
	quit           chan struct{}
	closed         bool
	activeConns    map[net.Conn]struct{}
}

// NewMockTCPServer starts a new TCP listener on a free loopback port (127.0.0.1:0).
func NewMockTCPServer() (*MockTCPServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("failed to listen on 127.0.0.1:0: %w", err)
	}

	tcpAddr := ln.Addr().(*net.TCPAddr)

	m := &MockTCPServer{
		listener:      ln,
		addr:          tcpAddr.String(),
		port:          tcpAddr.Port,
		connCh:        make(chan struct{}, 100),
		expectMatches: make(map[string]string),
		quit:          make(chan struct{}),
		activeConns:   make(map[net.Conn]struct{}),
	}

	go m.serve()
	return m, nil
}

// serve runs the TCP accept loop in a background goroutine.
func (m *MockTCPServer) serve() {
	for {
		conn, err := m.listener.Accept()
		if err != nil {
			select {
			case <-m.quit:
				return
			default:
				return
			}
		}

		m.mu.Lock()
		m.activeConns[conn] = struct{}{}
		m.mu.Unlock()

		go m.handleConn(conn)
	}
}

// handleConn manages a single incoming TCP client connection.
func (m *MockTCPServer) handleConn(conn net.Conn) {
	defer func() {
		m.mu.Lock()
		delete(m.activeConns, conn)
		m.mu.Unlock()
		_ = conn.Close()
	}()

	m.mu.Lock()
	m.connections = append(m.connections, TCPConnRecord{
		RemoteAddr: conn.RemoteAddr().String(),
		Timestamp:  time.Now(),
	})
	recordIdx := len(m.connections) - 1

	select {
	case m.connCh <- struct{}{}:
	default:
	}

	immClose := m.immediateClose
	hang := m.hangMode
	banner := m.banner
	delay := m.delay
	echo := m.echoMode
	matches := make(map[string]string, len(m.expectMatches))
	for k, v := range m.expectMatches {
		matches[k] = v
	}
	m.mu.Unlock()

	// Immediate close
	if immClose {
		return
	}

	// Hang mode: hold connection open until server shutdown
	if hang {
		<-m.quit
		return
	}

	// Simulated delay before interaction
	if delay > 0 {
		time.Sleep(delay)
	}

	// Send banner if configured
	if banner != "" {
		if _, err := conn.Write([]byte(banner)); err != nil {
			return
		}
	}

	// Read client payload with buffer
	buf := make([]byte, 4096)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, err := conn.Read(buf)
		if n > 0 {
			received := make([]byte, n)
			copy(received, buf[:n])

			m.mu.Lock()
			if recordIdx < len(m.connections) {
				m.connections[recordIdx].ReceivedPayload = append(m.connections[recordIdx].ReceivedPayload, received...)
			}
			m.mu.Unlock()

			if echo {
				if _, writeErr := conn.Write(received); writeErr != nil {
					return
				}
			}

			// Expect matching
			reqStr := string(received)
			for expect, resp := range matches {
				if strings.Contains(reqStr, expect) {
					if _, writeErr := conn.Write([]byte(resp)); writeErr != nil {
						return
					}
					break
				}
			}
		}

		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return
			}
			// If timeout and no echo/expect match pending, break
			return
		}
	}
}

// Addr returns the host:port string of the mock server.
func (m *MockTCPServer) Addr() string {
	return m.addr
}

// Port returns the listening TCP port.
func (m *MockTCPServer) Port() int {
	return m.port
}

// HostPort returns host and port separately.
func (m *MockTCPServer) HostPort() (string, int) {
	host, portStr, _ := net.SplitHostPort(m.addr)
	p, _ := strconv.Atoi(portStr)
	return host, p
}

// Close gracefully stops the listener and terminates active connections.
func (m *MockTCPServer) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	close(m.quit)
	err := m.listener.Close()

	// Terminate active connections
	for conn := range m.activeConns {
		_ = conn.Close()
	}
	m.mu.Unlock()
	return err
}

// SetBanner sets a greeting banner string to send immediately upon connection acceptance.
func (m *MockTCPServer) SetBanner(banner string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.banner = banner
}

// SetEchoMode enables or disables echoing received data back to client.
func (m *MockTCPServer) SetEchoMode(echo bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.echoMode = echo
}

// SetExpectMatch configures response to send when client transmits a matching pattern.
func (m *MockTCPServer) SetExpectMatch(expect, response string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expectMatches[expect] = response
}

// SetImmediateClose causes the server to close sockets immediately after accept.
func (m *MockTCPServer) SetImmediateClose(immClose bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.immediateClose = immClose
}

// SetHangMode keeps client connections open indefinitely without writing.
func (m *MockTCPServer) SetHangMode(hang bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hangMode = hang
}

// SetDelay introduces a delay before sending greeting or processing reads.
func (m *MockTCPServer) SetDelay(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.delay = d
}

// ConnectionCount returns the total number of accepted connections.
func (m *MockTCPServer) ConnectionCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.connections)
}

// GetConnections returns a snapshot slice of all recorded connections.
func (m *MockTCPServer) GetConnections() []TCPConnRecord {
	m.mu.RLock()
	defer m.mu.RUnlock()
	res := make([]TCPConnRecord, len(m.connections))
	copy(res, m.connections)
	return res
}

// WaitForConnection waits until at least one new connection arrives or timeout expires.
func (m *MockTCPServer) WaitForConnection(timeout time.Duration) error {
	select {
	case <-m.connCh:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("timeout waiting for TCP connection after %s", timeout)
	}
}

// Reset clears recorded connections.
func (m *MockTCPServer) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connections = nil
	// Flush channel
	for {
		select {
		case <-m.connCh:
		default:
			return
		}
	}
}
