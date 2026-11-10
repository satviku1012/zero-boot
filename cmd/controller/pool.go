// pool.go holds the warm pool: a buffered channel of frozen, ready-to-wake
// sandboxes spread across the worker nodes.
//
// The channel is the whole queue. Sending parks a lease; a select with a
// default case either takes a lease instantly or reports "empty" instantly.
// The channel does its own locking, so the claim path needs no mutex.
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"strconv"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/satviku1012/distributed-sandbox-pool/proto"
)

// SandboxLease identifies one sandbox and where it lives.
type SandboxLease struct {
	SandboxID string
	Worker    *worker // the node agent that owns the process
	TCPAddr   string  // "<worker host>:<sandbox port>", dialed by the gateway
}

// spawn asks w for a new warm (frozen) sandbox. Takes ~450ms: this is the
// cold start the pool exists to keep off the claim path.
func (c *controller) spawn(ctx context.Context, w *worker, opts ...grpc.CallOption) (SandboxLease, error) {
	ctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	resp, err := w.client.CreateWarmSandbox(ctx, &pb.CreateWarmSandboxRequest{}, opts...)
	if err != nil {
		return SandboxLease{}, fmt.Errorf("create on %s: %w", w.addr, err)
	}
	// The agent only reports a port; the controller supplies the host it
	// already uses to reach that worker.
	addr := net.JoinHostPort(w.host, strconv.Itoa(int(resp.Port)))
	return SandboxLease{SandboxID: resp.SandboxId, Worker: w, TCPAddr: addr}, nil
}

// activate thaws the lease's sandbox on the worker that owns it.
func (c *controller) activate(ctx context.Context, l SandboxLease) error {
	ctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	_, err := l.Worker.client.ActivateSandbox(ctx, &pb.ActivateSandboxRequest{SandboxId: l.SandboxID})
	return err
}

// replenishLoop keeps the pool full in the background, which also fills it
// at startup. It wakes when ClaimSandbox signals that a lease was taken, or
// on a ticker that retries failed spawns (e.g. agents that weren't up yet).
//
// Invariant: len(warmPool) + inFlight <= poolSize. Only this goroutine
// starts spawns, so its check-then-increment can't race another starter,
// which guarantees a finished spawn's send into warmPool never blocks.
func (c *controller) replenishLoop() {
	ticker := time.NewTicker(replenishInterval)
	defer ticker.Stop()
	for {
		for len(c.warmPool)+int(c.inFlight.Load()) < poolSize {
			c.inFlight.Add(1)
			go c.spawnIntoPool(c.pickWorker()) // round-robin across nodes
		}
		select {
		case <-c.refill:
		case <-ticker.C:
		}
	}
}

// spawnIntoPool creates one warm sandbox on w and parks it in the pool.
func (c *controller) spawnIntoPool(w *worker) {
	// Decrement only after the send. For a moment the new lease is counted
	// twice, which can only make the loop under-spawn (harmless); counting
	// it zero times could let the loop over-spawn and block a send.
	defer c.inFlight.Add(-1)
	// WaitForReady: if the agent isn't reachable yet (startup order, restart),
	// wait for the connection (up to rpcTimeout) instead of failing instantly.
	// Only background spawns do this; a claim's cold start should fail fast.
	lease, err := c.spawn(context.Background(), w, grpc.WaitForReady(true))
	if err != nil {
		log.Printf("replenish: %v", err)
		return
	}
	c.warmPool <- lease
	log.Printf("pooled sandbox %s at %s (%d/%d warm)", lease.SandboxID, lease.TCPAddr, len(c.warmPool), poolSize)
}

// signalRefill nudges replenishLoop without ever blocking the claim path:
// refill has capacity 1, so extra signals while one is pending are dropped.
func (c *controller) signalRefill() {
	select {
	case c.refill <- struct{}{}:
	default:
	}
}

// ClaimSandbox hands the gateway a running sandbox: warm if the pool has
// one, otherwise freshly spawned. Either way the request is served.
func (c *controller) ClaimSandbox(ctx context.Context, _ *pb.ClaimSandboxRequest) (*pb.ClaimSandboxResponse, error) {
	start := time.Now()

	// WARM_HIT: non-blocking receive. If a lease is parked we take it;
	// otherwise the default case runs immediately and we never wait.
	select {
	case lease := <-c.warmPool:
		c.signalRefill() // a slot just opened; refill it in the background
		err := c.activate(ctx, lease)
		if err == nil {
			c.trackLease(lease)
			log.Printf("WARM_HIT %s on %s in %v", lease.SandboxID, lease.Worker.host, time.Since(start))
			return claimResponse(lease, pb.ClaimStatus_WARM_HIT), nil
		}
		// The pooled sandbox is unusable (e.g. its agent restarted).
		// Best-effort cleanup, then serve the request with a cold start.
		log.Printf("warm sandbox %s failed to activate (%v), cold-starting instead", lease.SandboxID, err)
		go c.destroy(context.Background(), lease)
	default:
	}

	// COLD_MISS: pool is empty, so spawn and wake one synchronously
	// (~450ms). Slower, but burst traffic is never turned away.
	lease, err := c.spawn(ctx, c.pickWorker())
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "cold start: %v", err)
	}
	if err := c.activate(ctx, lease); err != nil {
		return nil, status.Errorf(codes.Unavailable, "activate %s: %v", lease.SandboxID, err)
	}
	c.trackLease(lease)
	log.Printf("COLD_MISS %s on %s in %v", lease.SandboxID, lease.Worker.host, time.Since(start))
	return claimResponse(lease, pb.ClaimStatus_COLD_MISS), nil
}

// claimResponse converts a lease into the RPC reply.
func claimResponse(l SandboxLease, s pb.ClaimStatus) *pb.ClaimSandboxResponse {
	return &pb.ClaimSandboxResponse{
		SandboxId:  l.SandboxID,
		Node:       l.Worker.host,
		TcpAddress: l.TCPAddr,
		Status:     s,
	}
}
