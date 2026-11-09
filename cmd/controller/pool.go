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
func (c *controller) spawn(ctx context.Context, w *worker) (SandboxLease, error) {
	ctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	resp, err := w.client.CreateWarmSandbox(ctx, &pb.CreateWarmSandboxRequest{})
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

// prewarm fills the pool at startup, spreading spawns across workers
// round-robin. Spawns run in parallel and in the background, so the server
// can serve right away; claims that arrive first simply take the cold path.
func (c *controller) prewarm() {
	for i := 0; i < poolSize; i++ {
		go func(w *worker) {
			lease, err := c.spawn(context.Background(), w)
			if err != nil {
				log.Printf("prewarm: %v", err)
				return
			}
			// Never blocks: exactly poolSize spawns into a poolSize-capacity
			// channel, and claims only ever remove.
			c.warmPool <- lease
			log.Printf("pooled sandbox %s at %s (%d/%d warm)", lease.SandboxID, lease.TCPAddr, len(c.warmPool), poolSize)
		}(c.pickWorker())
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
		err := c.activate(ctx, lease)
		if err == nil {
			log.Printf("WARM_HIT %s on %s in %v", lease.SandboxID, lease.Worker.host, time.Since(start))
			return claimResponse(lease, pb.ClaimStatus_WARM_HIT), nil
		}
		// The pooled sandbox is unusable (e.g. its agent restarted).
		// Best-effort cleanup, then serve the request with a cold start.
		log.Printf("warm sandbox %s failed to activate (%v), cold-starting instead", lease.SandboxID, err)
		go lease.Worker.client.DestroySandbox(context.Background(), &pb.DestroySandboxRequest{SandboxId: lease.SandboxID})
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
