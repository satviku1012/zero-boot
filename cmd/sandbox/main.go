// Command sandbox is the workload process that ZeroBoot keeps warm.
//
// It stands in for an expensive runtime (a language VM, an image loading its
// dependencies, ...): it spends -init-delay "initializing", then serves
// newline-delimited payloads over a raw TCP socket, one result line per payload.
//
// Contract with the node agent:
//   - started as: sandbox -port <n> -id <sandbox_id> [-init-delay <d>]
//   - prints exactly one line, "READY", to stdout once it is listening
//   - never writes to stdout again: the agent stops reading after READY, so
//     more output could fill the pipe and block us. Logs go to stderr.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"
)

// defaultInitDelay is the simulated initialization cost a COLD_MISS pays and
// a WARM_HIT skips, because warm sandboxes paid it before being frozen.
// 450ms is the measured median for a fresh `docker run` of Python importing
// numpy + pandas; see docs/cold-start-baseline.md for the method and data.
const defaultInitDelay = 450 * time.Millisecond

func main() {
	port := flag.Int("port", 9001, "TCP port to listen on")
	id := flag.String("id", "local", "sandbox ID assigned by the node agent")
	initDelay := flag.Duration("init-delay", defaultInitDelay, "simulated cold-start time")
	flag.Parse()
	log.SetPrefix(fmt.Sprintf("[sandbox %s] ", *id))

	// 1. Simulate booting a runtime.
	time.Sleep(*initDelay)

	// 2. Bind the listener *before* announcing readiness, so the agent never
	//    hands out an address that would refuse connections. If the port is
	//    taken we exit non-zero, and the agent sees EOF instead of READY.
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		log.Fatalf("listen: %v", err)
	}

	// 3. Tell the agent we're initialized and safe to freeze.
	fmt.Println("READY")

	// 4. Serve forever. Each gateway request opens its own connection; the
	//    agent ends our life with SIGKILL, so there is no shutdown path.
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			continue
		}
		go handle(conn, *id)
	}
}

// handle reads payload lines from one connection and writes back one result
// line per payload until the client closes its side.
func handle(conn net.Conn, id string) {
	defer conn.Close()

	// Inside the agent's CLONE_NEWPID namespace this prints pid=1, which is
	// an easy way to show the isolation is real.
	pid := os.Getpid()

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		result := execute(scanner.Text())
		fmt.Fprintf(conn, "sandbox=%s pid=%d result=%s\n", id, pid, result)
	}
	if err := scanner.Err(); err != nil {
		log.Printf("read: %v", err)
	}
}

// execute stands in for real code execution. Uppercasing is enough to prove
// the payload made the round trip through this process.
func execute(payload string) string {
	return strings.ToUpper(payload)
}
