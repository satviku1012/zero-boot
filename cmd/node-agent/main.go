// Command node-agent runs on every worker node and owns that node's sandbox
// processes. It serves NodeAgentService over gRPC on :50052; the controller
// is its only client.
//
// Sandbox lifecycle (process details live in sandbox.go):
//
//	CreateWarmSandbox: spawn (namespaced) -> wait for READY -> cgroup -> freeze
//	ActivateSandbox:   thaw
//	DestroySandbox:    thaw -> SIGKILL -> reap -> remove cgroup
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/satviku1012/distributed-sandbox-pool/proto"
)

const (
	listenAddr = ":50052"
	portMin    = 9001 // sandbox TCP ports are handed out from this range
	portMax    = 9099
)

// agent implements pb.NodeAgentServiceServer.
type agent struct {
	pb.UnimplementedNodeAgentServiceServer
	binary string // path to the sandbox executable

	mu        sync.Mutex // guards both maps below
	sandboxes map[string]*sandbox
	ports     map[int]bool // ports owned by a live sandbox
}

func (a *agent) CreateWarmSandbox(ctx context.Context, _ *pb.CreateWarmSandboxRequest) (*pb.CreateWarmSandboxResponse, error) {
	port, err := a.reservePort()
	if err != nil {
		return nil, status.Error(codes.ResourceExhausted, err.Error())
	}
	id := newID()

	// Takes ~2s: the sandbox's cold start happens here, off the claim path.
	sb, err := spawnSandbox(a.binary, id, port)
	if err != nil {
		a.releasePort(port)
		return nil, status.Errorf(codes.Internal, "sandbox %s: %v", id, err)
	}

	a.mu.Lock()
	a.sandboxes[id] = sb
	a.mu.Unlock()
	log.Printf("created sandbox %s on port %d (frozen, cgroup=%v)", id, port, sb.cgroup)
	return &pb.CreateWarmSandboxResponse{SandboxId: id, Port: int32(port)}, nil
}

func (a *agent) ActivateSandbox(ctx context.Context, req *pb.ActivateSandboxRequest) (*pb.ActivateSandboxResponse, error) {
	a.mu.Lock()
	sb, ok := a.sandboxes[req.SandboxId]
	a.mu.Unlock()
	if !ok {
		return nil, status.Errorf(codes.NotFound, "sandbox %s not found", req.SandboxId)
	}
	if err := sb.setFrozen(false); err != nil {
		return nil, status.Errorf(codes.Internal, "thaw %s: %v", sb.id, err)
	}
	return &pb.ActivateSandboxResponse{}, nil
}

func (a *agent) DestroySandbox(ctx context.Context, req *pb.DestroySandboxRequest) (*pb.DestroySandboxResponse, error) {
	// Remove from the map first so no concurrent Activate can find it.
	a.mu.Lock()
	sb, ok := a.sandboxes[req.SandboxId]
	delete(a.sandboxes, req.SandboxId)
	a.mu.Unlock()
	if !ok {
		return nil, status.Errorf(codes.NotFound, "sandbox %s not found", req.SandboxId)
	}
	sb.kill()
	a.releasePort(sb.port)
	log.Printf("destroyed sandbox %s", sb.id)
	return &pb.DestroySandboxResponse{}, nil
}

// reservePort claims the lowest free port in [portMin, portMax].
func (a *agent) reservePort() (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for p := portMin; p <= portMax; p++ {
		if !a.ports[p] {
			a.ports[p] = true
			return p, nil
		}
	}
	return 0, errors.New("no free sandbox ports")
}

func (a *agent) releasePort(port int) {
	a.mu.Lock()
	delete(a.ports, port)
	a.mu.Unlock()
}

func main() {
	binary := os.Getenv("SANDBOX_BINARY")
	if binary == "" {
		binary = "./bin/sandbox"
	}
	a := &agent{binary: binary, sandboxes: map[string]*sandbox{}, ports: map[int]bool{}}

	lis, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("listen %s: %v", listenAddr, err)
	}
	srv := grpc.NewServer()
	pb.RegisterNodeAgentServiceServer(srv, a)

	// On Ctrl-C / docker stop, kill every sandbox so none are left frozen
	// and holding ports, then stop serving (which makes Serve return).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		a.mu.Lock()
		for _, sb := range a.sandboxes {
			sb.kill()
		}
		a.mu.Unlock()
		srv.Stop()
	}()

	log.Printf("node-agent listening on %s (sandbox binary: %s)", listenAddr, binary)
	if err := srv.Serve(lis); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
