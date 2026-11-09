# ZeroBoot Implementation Plan

This plan turns the requirements in `PROJECT.md` into sequential phases. Complete and verify each phase before starting the next. Keep the implementation within the specified Go standard library, gRPC, and protobuf dependencies; preserve macOS build safety for Linux-only isolation code.

## Phase 1: Module and gRPC Contract

**Scope**

- Establish the Go module and repository ignore rules.
- Define `NodeAgentService` and `ControllerService` messages and RPCs in `proto/zeroboot.proto`.
- Generate and commit the Go protobuf and gRPC bindings.

**Verification**

- Run `go mod tidy` and confirm it exits successfully.
- Run `go test ./...` (or `go build ./...` if no tests exist yet) to confirm the generated contract compiles.
- Inspect generated bindings to confirm all five required RPCs are present: create, activate, destroy, claim, and release.

**Definition of Done**

- The module and protobuf contract exist, generated bindings compile, and the five RPCs have request and response types sufficient to carry sandbox identity, worker TCP address, and claim status (`WARM_HIT` or `COLD_MISS`).

## Phase 2: Sandbox Workload and Worker Node Agent

**Scope**

- Implement `cmd/sandbox`: simulate a startup delay (`-init-delay`, default 450ms from `docs/cold-start-baseline.md`), listen on its assigned TCP port, and process/echo payloads.
- Implement worker process lifecycle and Linux isolation in `cmd/node-agent/linux_isolation.go`: PID/UTS namespaces, waiting for the sandbox's `READY` line, a per-sandbox cgroup with freeze/unfreeze, and destruction. No memory limit is set (see `PROJECT.md` §2).
- Provide a non-Linux build stub or equivalent build-tagged separation so development builds remain portable.
- Expose create, activate, and destroy through the node-agent gRPC server on port `50052`.

**Verification**

- Build the sandbox and node-agent packages on the development host.
- On a Linux host/container with the needed privileges, create a sandbox and confirm it reports ready only after initialization, accepts a TCP payload, and returns the expected response.
- Confirm the agent returns only after `READY` is printed, and that a sandbox that never prints `READY` is killed after the timeout.
- Confirm the sandbox PID is listed in `zeroboot_<id>/cgroup.procs` and `cgroup.freeze` transitions to `1` while idle and `0` on activation; verify the documented `SIGSTOP`/`SIGCONT` fallback when cgroup writes are unavailable.
- Confirm destroy terminates the process and removes its cgroup directory where applicable.

**Definition of Done**

- A worker can create, activate, and destroy a sandbox through its gRPC API; Linux isolation and the local-development fallback both behave as specified, and non-Linux builds compile.

## Phase 3: Controller and Distributed Warm Pool

**Scope**

- Parse `WORKER_NODES` and establish gRPC clients to the configured agents.
- Maintain a capacity-five `chan SandboxLease` and seed the pool across workers at startup.
- Implement `ClaimSandbox` with a non-blocking channel receive: activate and return a warm lease when available; otherwise synchronously create and activate a cold lease.
- Return the lease ID, worker identity, TCP endpoint, and `WARM_HIT`/`COLD_MISS` status. Build the TCP endpoint from the host in the lease's `WORKER_NODES` entry plus the port the agent returned.

**Verification**

- Build the controller and run it with one and then multiple reachable worker agents.
- Claim from a populated pool and confirm the warm lease is activated and reports `WARM_HIT`.
- Empty the pool and claim again; confirm the request completes by creating a lease and reports `COLD_MISS`.
- Confirm the pool never contains more than five leases and claims route to the worker that owns each lease.

**Definition of Done**

- The controller serves claims over gRPC, distributes warm sandboxes among configured workers, and handles pool exhaustion with a working synchronous cold-start path.

## Phase 4: Replenishment, Release, and Lease Expiration

**Scope**

- Add asynchronous replenishment that keeps `len(warmPool) + inFlightSpawns` at the target capacity without over-spawning.
- Track active leases in a mutex-protected map.
- Implement `ReleaseSandbox` to destroy the sandbox and remove its active lease.
- Add cleanup polling every two seconds and destroy leases checked out for more than ten seconds.

**Verification**

- Claim a warm lease and verify background replenishment restores the pool to capacity five.
- Issue concurrent claims and confirm no duplicate lease assignment or pool overfill.
- Release a lease and confirm it is destroyed and removed from active tracking.
- Leave a lease un-released; verify cleanup destroys it after its TTL and removes it from tracking.

**Definition of Done**

- Pool capacity recovers asynchronously, lease tracking remains safe under concurrent claims/releases, and both explicit release and the ten-second timeout reclaim worker processes.

## Phase 5: HTTP Gateway and Direct TCP Streaming

**Scope**

- Expose `POST /execute` on port `8080` and validate/parse its payload.
- Claim a sandbox through controller gRPC and measure allocation latency.
- Dial the worker TCP endpoint directly with `net.DialTCP`, enable `TCP_NODELAY` and keepalive, stream the payload, and read the result.
- Release the lease after normal completion; retain it when `simulate_hang=true` so controller cleanup can be exercised.
- Return JSON containing sandbox ID, node, status, allocation latency, and output.

**Verification**

- Send a normal request and confirm its payload reaches the worker over TCP and the JSON response contains all specified fields.
- Confirm a warm request reports `WARM_HIT` and allocation latency below 5 ms under the benchmark conditions; confirm an empty-pool request reports `COLD_MISS` and includes the expected ~450ms startup.
- Confirm normal requests release their lease and `simulate_hang=true` requests are reclaimed by controller timeout cleanup.
- Confirm the gateway sets `TCP_NODELAY` and keepalive on its direct worker connection.

**Definition of Done**

- The gateway completes the HTTP-to-gRPC-to-direct-TCP path, reports the required metrics, releases completed leases, and supports timeout-cleanup verification.

## Phase 6: Multi-Node Deployment and Operational Benchmark

**Scope**

- Add a multi-stage Dockerfile that builds all four binaries.
- Add Docker Compose services for two privileged Linux workers, the controller, and gateway on a shared bridge network, with cgroup v2 mounted for workers.
- Add `scripts/benchmark.sh` for five warm requests, three burst requests beyond pool capacity, and one hanging request.
- Document prerequisites, startup, use, architecture, isolation limitations, benchmark procedure, and expected outcomes in `README.md`.

**Verification**

- Build and start the Compose cluster; confirm both workers register/connect and the gateway and controller become reachable.
- Run the benchmark script and verify it records five warm claims, three cold-miss burst claims, and the hanging request cleanup after the configured TTL.
- Confirm observed allocation timings are reported in the benchmark output, with the warm target below 5 ms and cold startup near 450ms (allowing for environment and scheduling overhead).
- Follow the README from a clean checkout to start the cluster, execute a request, run the benchmark, and shut the cluster down.

**Definition of Done**

- A documented, repeatable Compose deployment runs across two worker nodes, and the benchmark exercises warm allocation, capacity overflow, and timeout cleanup with visible results.

## Completion Gate

The project is complete when all six phase Definitions of Done are met and the three architectural guarantees in `PROJECT.md` are demonstrated end to end: distributed warm allocation through gRPC, Linux namespace/cgroup isolation, and direct TCP streaming with bounded lease cleanup.
