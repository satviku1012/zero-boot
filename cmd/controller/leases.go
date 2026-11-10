// leases.go tracks sandboxes that have been handed out, and makes sure each
// one is eventually destroyed: explicitly via ReleaseSandbox, or by
// cleanupLoop once it has been checked out longer than leaseTTL (e.g. the
// gateway crashed or a task hung).
//
// Unlike the warm pool, this is a map that two goroutines touch (claim and
// release handlers on one side, cleanupLoop on the other), so a sync.Mutex
// guards it.
package main

import (
	"context"
	"log"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/satviku1012/distributed-sandbox-pool/proto"
)

// ActiveLease is a claimed sandbox plus the moment it was claimed.
type ActiveLease struct {
	SandboxLease
	ClaimedAt time.Time
}

// trackLease records a freshly claimed sandbox so it can be released or
// expired later.
func (c *controller) trackLease(l SandboxLease) {
	c.mu.Lock()
	c.activeLeases[l.SandboxID] = ActiveLease{SandboxLease: l, ClaimedAt: time.Now()}
	c.mu.Unlock()
}

// ReleaseSandbox is called by the gateway when a task finishes. Sandboxes
// are never reused (no state can leak between tasks), so release = destroy.
func (c *controller) ReleaseSandbox(ctx context.Context, req *pb.ReleaseSandboxRequest) (*pb.ReleaseSandboxResponse, error) {
	// Remove under the lock, destroy outside it: the RPC to the worker
	// shouldn't block other claims and releases.
	c.mu.Lock()
	lease, ok := c.activeLeases[req.SandboxId]
	delete(c.activeLeases, req.SandboxId)
	c.mu.Unlock()
	if !ok {
		return nil, status.Errorf(codes.NotFound, "sandbox %s is not claimed (already released or expired)", req.SandboxId)
	}

	if err := c.destroy(ctx, lease.SandboxLease); err != nil {
		return nil, status.Errorf(codes.Unavailable, "destroy %s: %v", req.SandboxId, err)
	}
	log.Printf("released %s after %v", req.SandboxId, time.Since(lease.ClaimedAt).Round(time.Millisecond))
	return &pb.ReleaseSandboxResponse{}, nil
}

// cleanupLoop runs every cleanupInterval and destroys any lease held longer
// than leaseTTL, so a hung or abandoned task can't hold a sandbox forever.
func (c *controller) cleanupLoop() {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()
	for range ticker.C {
		// Collect and remove expired leases under the lock (deleting from a
		// map while ranging over it is safe in Go)...
		var expired []ActiveLease
		c.mu.Lock()
		for id, l := range c.activeLeases {
			if time.Since(l.ClaimedAt) > leaseTTL {
				expired = append(expired, l)
				delete(c.activeLeases, id)
			}
		}
		c.mu.Unlock()

		// ...then make the slow worker RPCs without holding it.
		for _, l := range expired {
			log.Printf("lease %s expired after %v, destroying", l.SandboxID, time.Since(l.ClaimedAt).Round(time.Millisecond))
			if err := c.destroy(context.Background(), l.SandboxLease); err != nil {
				log.Printf("cleanup %s: %v", l.SandboxID, err)
			}
		}
	}
}

// destroy kills a sandbox on the worker that owns it.
func (c *controller) destroy(ctx context.Context, l SandboxLease) error {
	ctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	_, err := l.Worker.client.DestroySandbox(ctx, &pb.DestroySandboxRequest{SandboxId: l.SandboxID})
	return err
}
