// Embedded scripts B2 execs inside its client/echo containers, mirroring
// benchmarks/b1's pattern (see that package's scripts.go doc comment) but
// with a CONCURRENT echo server: B2 measures sustained throughput under N
// simultaneous connections, which b1's single-connection-at-a-time server
// (adequate for b1's one-client-at-a-time fault measurements) cannot serve.
package main

// echoServerPy is a concurrent TCP echo service: one thread per accepted
// connection, so N load-generator workers can hold N simultaneous
// connections open and all get served, which is what "max sustained rps"
// actually requires exercising.
const echoServerPy = `
import socket
import threading

s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("0.0.0.0", 9000))
s.listen(256)

def handle(conn):
    try:
        while True:
            data = conn.recv(65536)
            if not data:
                break
            conn.sendall(data)
    except Exception:
        pass
    finally:
        try:
            conn.close()
        except Exception:
            pass

while True:
    conn, _ = s.accept()
    threading.Thread(target=handle, args=(conn,), daemon=True).start()
`

// loadClientPy runs argv[3] concurrent worker threads against argv[1]:argv[2]
// for argv[4] seconds: each worker holds one persistent connection open and
// sends a small fixed payload back and forth as fast as it can, timing every
// round trip. It prints one JSON object with every measured round-trip
// latency (milliseconds) and the actual wall-clock window the workers ran
// in, so the Go side computes p50/p95/p99 and requests/sec (= n /
// wall_seconds, the ACTUAL elapsed time, not the requested duration) from
// real data rather than assuming the requested duration was hit exactly.
const loadClientPy = `
import socket, sys, time, json, threading

host = sys.argv[1]
port = int(sys.argv[2])
workers = int(sys.argv[3])
duration = float(sys.argv[4])
payload = b"x" * 64

results = [[] for _ in range(workers)]
errors = [0] * workers

def worker(idx):
    try:
        conn = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        conn.settimeout(5)
        conn.connect((host, port))
    except Exception:
        errors[idx] += 1
        return
    stop_at = time.time() + duration
    try:
        while time.time() < stop_at:
            t0 = time.perf_counter()
            conn.sendall(payload)
            got = 0
            while got < len(payload):
                chunk = conn.recv(len(payload) - got)
                if not chunk:
                    raise ConnectionError("short read")
                got += len(chunk)
            t1 = time.perf_counter()
            results[idx].append((t1 - t0) * 1000.0)
    except Exception:
        errors[idx] += 1
    finally:
        try:
            conn.close()
        except Exception:
            pass

threads = [threading.Thread(target=worker, args=(i,)) for i in range(workers)]
wall_start = time.time()
for t in threads:
    t.start()
for t in threads:
    t.join()
wall_end = time.time()

all_latencies = [v for sub in results for v in sub]
print(json.dumps({
    "latencies_ms": all_latencies,
    "n": len(all_latencies),
    "wall_seconds": wall_end - wall_start,
    "worker_errors": sum(errors),
}))
`
