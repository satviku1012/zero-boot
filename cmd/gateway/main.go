// Command gateway is ZeroBoot's HTTP entry point. POST /execute claims a
// sandbox from the controller over gRPC, streams the task straight to that
// sandbox over raw TCP (stream.go), releases the sandbox, and replies with
// JSON metrics.
//
// Files:
//
//	main.go:   config, HTTP server, the /execute handler (gRPC control plane)
//	stream.go: the raw TCP exchange with a sandbox (data plane)
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/satviku1012/distributed-sandbox-pool/proto"
)

const (
	listenAddr   = ":8080"
	maxBodyBytes = 64 << 10         // reject request bodies over 64 KiB
	rpcTimeout   = 10 * time.Second // cap on one controller call (a cold start takes ~450ms)
)

// executeRequest is the POST /execute body, e.g. {"payload": "hello"}.
type executeRequest struct {
	Payload string `json:"payload"`
}

// executeResponse is the JSON returned for each task.
type executeResponse struct {
	SandboxID           string  `json:"sandbox_id"`
	Node                string  `json:"node"`
	Status              string  `json:"status"` // WARM_HIT or COLD_MISS
	AllocationLatencyMs float64 `json:"allocation_latency_ms"`
	Output              string  `json:"output"`
}

type gateway struct {
	controller pb.ControllerServiceClient
}

func (g *gateway) execute(w http.ResponseWriter, r *http.Request) {
	var req executeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, `body must be JSON like {"payload": "..."}: `+err.Error())
		return
	}
	// The sandbox reads exactly one line, so the payload must be one line.
	if req.Payload == "" || strings.ContainsAny(req.Payload, "\r\n") {
		writeError(w, http.StatusBadRequest, "payload must be a non-empty single line")
		return
	}
	hang := r.URL.Query().Get("simulate_hang") == "true"

	// Allocation latency is the ClaimSandbox round trip: how long until we
	// hold a running sandbox. The context is not tied to the HTTP request,
	// so a client hanging up mid-claim can't strand a half-made sandbox.
	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()
	start := time.Now()
	claim, err := g.controller.ClaimSandbox(ctx, &pb.ClaimSandboxRequest{})
	alloc := time.Since(start)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "claim sandbox: "+err.Error())
		return
	}

	output, streamErr := streamTask(claim.TcpAddress, req.Payload)

	// Release even if streaming failed, so the sandbox doesn't wait out the
	// TTL. simulate_hang skips it, leaving the lease for the controller's
	// 10s cleanup loop to reclaim (what the benchmark verifies).
	if hang {
		log.Printf("simulate_hang: leaving %s claimed for TTL cleanup", claim.SandboxId)
	} else {
		g.release(claim.SandboxId)
	}

	if streamErr != nil {
		writeError(w, http.StatusBadGateway, "stream to "+claim.TcpAddress+": "+streamErr.Error())
		return
	}
	writeJSON(w, http.StatusOK, executeResponse{
		SandboxID:           claim.SandboxId,
		Node:                claim.Node,
		Status:              claim.Status.String(),
		AllocationLatencyMs: float64(alloc.Microseconds()) / 1000, // ms with µs precision
		Output:              output,
	})
}

// release tells the controller the task is done, which destroys the sandbox.
// A failure is only logged: the task already succeeded, and the controller's
// TTL cleanup is the backstop.
func (g *gateway) release(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()
	if _, err := g.controller.ReleaseSandbox(ctx, &pb.ReleaseSandboxRequest{SandboxId: id}); err != nil {
		log.Printf("release %s: %v", id, err)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func main() {
	addr := os.Getenv("CONTROLLER_ADDR")
	if addr == "" {
		addr = "localhost:50051"
	}
	// Lazy connect: the gateway starts even if the controller isn't up yet.
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("dial controller %s: %v", addr, err)
	}
	g := &gateway{controller: pb.NewControllerServiceClient(conn)}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /execute", g.execute) // other methods get 405
	srv := &http.Server{Addr: listenAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	log.Printf("gateway listening on %s, controller: %s", listenAddr, addr)
	log.Fatal(srv.ListenAndServe())
}
