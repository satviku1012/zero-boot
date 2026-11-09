# Cold-Start Baseline

The sandbox fakes its startup cost with `time.Sleep(-init-delay)`. This page records the real measurements behind the default, so the warm-vs-cold comparison doesn't depend on a made-up number.

## Result

**Default `-init-delay` = 450ms**, the median time to start a fresh Python container that imports numpy and pandas. That is the cold start a warm pool avoids: creating a brand-new execution environment for each request.

| Scenario | min | median | p90 | max |
|---|---|---|---|---|
| A. New Python process, warm page cache | 144 ms | 156 ms | 166 ms | 169 ms |
| B. New Python process, page cache dropped | 239 ms | 250 ms | 268 ms | 287 ms |
| **C. Fresh container + interpreter + imports** | **419 ms** | **455 ms** | **465 ms** | **467 ms** |

All three were measured on 2026-11-08 with 10 runs each, on an Apple M3 Pro (11 CPUs) running Docker Desktop 29.1.3 (Linux 6.12.54-linuxkit, cgroup v2), with Python 3.12.14, numpy 2.5.4 and pandas 3.1.1.

## What it means

- **ZeroBoot hides whatever startup costs.** A warm hit is a gRPC round trip plus a `cgroup.freeze` write, about 2ms in Step 2 testing, whether startup takes 450ms or 20s. The size of the speed-up depends on the workload.
- **Heavier runtimes cost more.** A JVM service or a PyTorch import takes seconds, not hundreds of milliseconds. To model one, pass a measured `-init-delay` rather than guessing.
- **Your number will differ.** Results depend on hardware and storage. Re-run the steps below on the machine you benchmark on and quote that number.

## How to reproduce

Build a throwaway image:

```sh
docker build -t zb-coldstart:tmp - <<'EOF'
FROM python:3.12-slim
RUN pip install --no-cache-dir numpy pandas
EOF
```

**A and B: a new process inside one running container.** Run this loop as `sh -c` inside the container. For B, add `sync; echo 3 > /proc/sys/vm/drop_caches` before each run and start the container with `--privileged`.

```sh
python -c "import numpy, pandas"            # prime once
for i in $(seq 10); do
  s=$(date +%s%N); python -c "import numpy, pandas"; e=$(date +%s%N)
  echo $(( (e - s) / 1000000 ))              # ms
done
```

**C: a fresh container each time, timed from the host.** Run `docker run --rm zb-coldstart:tmp true` once first so image pull time isn't counted.

```python
import subprocess, time, statistics
cmd = ["docker", "run", "--rm", "zb-coldstart:tmp", "python", "-c", "import numpy, pandas"]
ts = []
for _ in range(10):
    s = time.perf_counter(); subprocess.run(cmd, check=True)
    ts.append((time.perf_counter() - s) * 1000)
print(f"median={statistics.median(ts):.0f}ms")
```

Clean up with `docker rmi zb-coldstart:tmp`.
