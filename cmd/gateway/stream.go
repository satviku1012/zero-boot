// stream.go is the data plane: it carries one task to a sandbox over a raw
// TCP socket and reads back the result. The controller only said *where*
// the sandbox is; the payload itself never passes through the controller.
package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// streamTimeout bounds one whole exchange (write task, read result), so a
// stuck sandbox can't hold an HTTP request open forever.
const streamTimeout = 5 * time.Second

// streamTask sends payload as one line to the sandbox at addr (e.g.
// "node-1:9003") and returns the sandbox's one-line reply.
func streamTask(addr, payload string) (string, error) {
	tcpAddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("resolve: %w", err)
	}
	// DialTCP returns a *net.TCPConn, which exposes the TCP socket options
	// below (a plain net.Conn would not).
	conn, err := net.DialTCP("tcp", nil, tcpAddr)
	if err != nil {
		return "", fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	// Go already enables both options by default; they are set explicitly
	// so the choice is visible and can't silently change.
	//
	// TCP_NODELAY turns off Nagle's algorithm, which holds back small
	// writes (up to ~40ms when combined with delayed ACKs) hoping to batch
	// them. A one-line task is exactly such a small write, so send it now.
	if err := conn.SetNoDelay(true); err != nil {
		return "", fmt.Errorf("set TCP_NODELAY: %w", err)
	}
	// Keepalive makes the OS probe an idle connection, so a peer that
	// vanished (e.g. its node died) is noticed instead of waited on.
	if err := conn.SetKeepAlive(true); err != nil {
		return "", fmt.Errorf("set keepalive: %w", err)
	}
	if err := conn.SetDeadline(time.Now().Add(streamTimeout)); err != nil {
		return "", fmt.Errorf("set deadline: %w", err)
	}

	// The sandbox protocol is line-based: one line in, one line out.
	if _, err := io.WriteString(conn, payload+"\n"); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}
	return strings.TrimSuffix(line, "\n"), nil
}
