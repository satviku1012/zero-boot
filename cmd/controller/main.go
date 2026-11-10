// Command controller is ZeroBoot's central control plane. It serves
// ControllerService on :50051 for the gateway, and is a gRPC client of every
// worker node's NodeAgentService.
//
// Files:
//
//	main.go:   config, worker connections, gRPC server
//	pool.go:   the warm pool (buffered channel), replenishment, ClaimSandbox
//	leases.go: claimed-lease tracking, ReleaseSandbox, TTL cleanup
package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/satviku1012/distributed-sandbox-pool/proto"
)

const (
	listenAddr = ":50051"
	poolSize   = 5                // capacity of warmPool
	rpcTimeout = 10 * time.Second // cap on one agent call (a spawn takes ~450ms)

	replenishInterval = 1 * time.Second  // fallback refill check / spawn retry
	leaseTTL          = 10 * time.Second // max time a sandbox may stay claimed
	cleanupInterval   = 2 * time.Second  // how often expired leases are reaped
)

// worker is one node agent the controller can place sandboxes on.
type worker struct {
	addr   string // as listed in WORKER_NODES, e.g. "node-1:50052"
	host   string // host part ("node-1"), used to build sandbox TCP addresses
	client pb.NodeAgentServiceClient
}

// dialWorkers parses WORKER_NODES ("host:port,host:port") and creates one
// gRPC client per entry. grpc.NewClient connects lazily, so an agent that
// isn't up yet doesn't block the controller from starting.
func dialWorkers(list string) ([]*worker, error) {
	var workers []*worker
	for _, addr := range strings.Split(list, ",") {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("bad WORKER_NODES entry %q: %w", addr, err)
		}
		// Plaintext is fine on the private cluster network; TLS is out of scope.
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return nil, fmt.Errorf("dial %s: %w", addr, err)
		}
		workers = append(workers, &worker{addr: addr, host: host, client: pb.NewNodeAgentServiceClient(conn)})
	}
	if len(workers) == 0 {
		return nil, fmt.Errorf("WORKER_NODES lists no workers")
	}
	return workers, nil
}

// controller implements pb.ControllerServiceServer.
type controller struct {
	// Embedding is required by grpc-go for forward compatibility.
	pb.UnimplementedControllerServiceServer

	workers []*worker
	next    atomic.Uint64 // round-robin cursor into workers

	// Warm pool (pool.go). The channel needs no extra locking.
	warmPool chan SandboxLease // frozen sandboxes, ready to wake
	inFlight atomic.Int32      // spawns started but not yet in warmPool
	refill   chan struct{}     // ClaimSandbox -> replenishLoop wake-up signal

	// Claimed sandboxes (leases.go), keyed by sandbox ID.
	mu           sync.Mutex
	activeLeases map[string]ActiveLease
}

// pickWorker returns workers in round-robin order. The atomic counter makes
// it safe to call from many goroutines without a lock.
func (c *controller) pickWorker() *worker {
	n := c.next.Add(1) - 1
	return c.workers[n%uint64(len(c.workers))]
}

func main() {
	nodes := os.Getenv("WORKER_NODES")
	if nodes == "" {
		nodes = "localhost:50052"
	}
	workers, err := dialWorkers(nodes)
	if err != nil {
		log.Fatal(err)
	}

	c := &controller{
		workers:      workers,
		warmPool:     make(chan SandboxLease, poolSize),
		refill:       make(chan struct{}, 1),
		activeLeases: map[string]ActiveLease{},
	}
	go c.replenishLoop() // also does the initial fill
	go c.cleanupLoop()

	lis, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("listen %s: %v", listenAddr, err)
	}
	srv := grpc.NewServer()
	pb.RegisterControllerServiceServer(srv, c)

	log.Printf("controller listening on %s, workers: %s", listenAddr, nodes)
	if err := srv.Serve(lis); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
