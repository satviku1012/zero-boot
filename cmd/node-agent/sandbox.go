// sandbox.go manages a single sandbox process: spawning it inside its
// namespaces, waiting for READY, freezing/thawing it, and tearing it down.
//
// Freezing uses cgroup.freeze when /sys/fs/cgroup is writable and falls back
// to SIGSTOP/SIGCONT otherwise (macOS, or a container without cgroup access).
package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// readyTimeout bounds how long we wait for READY; generous next to the
// sandbox's 2s cold start, but short enough to fail a stuck spawn quickly.
const readyTimeout = 5 * time.Second

// sandbox is one running workload process on this node.
type sandbox struct {
	id     string
	port   int
	cmd    *exec.Cmd
	cgroup bool // true: freeze via cgroup.freeze; false: SIGSTOP/SIGCONT
}

// spawnSandbox starts the sandbox binary in its namespaces, waits until it
// prints READY, then moves it into its cgroup and freezes it.
func spawnSandbox(binary, id string, port int) (*sandbox, error) {
	// exec.Command, not CommandContext: the sandbox must outlive the RPC.
	cmd := exec.Command(binary, "-port", strconv.Itoa(port), "-id", id)
	cmd.SysProcAttr = isolationAttr() // CLONE_NEWPID | CLONE_NEWUTS on Linux
	cmd.Stderr = os.Stderr            // sandbox logs show up in the agent's logs
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	sb := &sandbox{id: id, port: port, cmd: cmd}
	if err := waitReady(stdout); err != nil {
		sb.kill()
		return nil, err
	}

	// Freeze only after READY, so we never freeze a half-initialized process.
	if err := cgroupAttach(id, cmd.Process.Pid); err == nil {
		sb.cgroup = true
	} else {
		log.Printf("sandbox %s: cgroup v2 unavailable (%v), using SIGSTOP/SIGCONT", id, err)
	}
	if err := sb.setFrozen(true); err != nil {
		sb.kill()
		return nil, fmt.Errorf("freeze: %w", err)
	}
	return sb, nil
}

// waitReady blocks until the sandbox prints READY, exits, or times out.
func waitReady(stdout io.Reader) error {
	result := make(chan error, 1) // buffered, so the reader can always finish
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		switch {
		case err != nil:
			result <- fmt.Errorf("exited before READY: %w", err)
		case strings.TrimSpace(line) != "READY":
			result <- fmt.Errorf("expected READY, got %q", line)
		default:
			result <- nil
		}
	}()
	select {
	case err := <-result:
		return err
	case <-time.After(readyTimeout):
		// The caller kills the process, which closes the pipe and unblocks
		// the reader goroutine above.
		return errors.New("timed out waiting for READY")
	}
}

// setFrozen pauses (true) or resumes (false) the sandbox.
func (s *sandbox) setFrozen(frozen bool) error {
	if s.cgroup {
		return cgroupSetFrozen(s.id, frozen)
	}
	sig := syscall.SIGCONT
	if frozen {
		sig = syscall.SIGSTOP
	}
	return s.cmd.Process.Signal(sig)
}

// kill thaws, kills, and reaps the process, then removes its cgroup.
// Errors are ignored: this is best-effort teardown of a process we own.
func (s *sandbox) kill() {
	s.setFrozen(false)
	s.cmd.Process.Kill() // SIGKILL
	s.cmd.Wait()         // reap, so no zombie is left behind
	if s.cgroup {
		cgroupRemove(s.id) // only succeeds once the cgroup has no live tasks
	}
}

// newID returns 8 random hex characters, unique enough across the cluster.
func newID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}
