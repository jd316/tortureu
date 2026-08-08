package main

// echoServerPy is the known-good TCP echo service every verb's fault path is
// measured against. It speaks two request shapes on one persistent
// connection: plain bytes are echoed back verbatim (latency/jitter/down/
// pause/kill), and "BULK<n>\n" triggers an n-byte reply (bandwidth).
const echoServerPy = `
import socket
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("0.0.0.0", 9000))
s.listen(20)
while True:
    conn, _ = s.accept()
    try:
        while True:
            data = conn.recv(65536)
            if not data:
                break
            if data.startswith(b"BULK"):
                try:
                    n = int(data[4:].strip())
                except Exception:
                    n = 0
                conn.sendall(b"X" * n)
            else:
                conn.sendall(data)
    except Exception:
        pass
    finally:
        try:
            conn.close()
        except Exception:
            pass
`

// pingClientPy opens one persistent connection to argv[1]:argv[2] and does
// argv[3] synchronous ping-pong round trips, timing each one INSIDE the
// container so the N-iteration loop is not swamped by docker-exec
// process-spawn overhead. Emits one JSON object with all samples (ms).
const pingClientPy = `
import socket, time, json, sys
host, port, n = sys.argv[1], int(sys.argv[2]), int(sys.argv[3])
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.settimeout(10)
s.connect((host, port))
samples = []
for i in range(n):
    t0 = time.perf_counter()
    s.sendall(b"ping")
    data = s.recv(64)
    t1 = time.perf_counter()
    samples.append((t1 - t0) * 1000.0)
s.close()
print(json.dumps({"samples": samples}))
`

// bandwidthClientPy requests one argv[3]-byte bulk reply from argv[1]:argv[2]
// and reports bytes/sec measured over the actual transfer.
const bandwidthClientPy = `
import socket, time, json, sys
host, port, size = sys.argv[1], int(sys.argv[2]), int(sys.argv[3])
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.settimeout(60)
s.connect((host, port))
s.sendall(("BULK%d\n" % size).encode())
received = 0
t0 = time.perf_counter()
while received < size:
    chunk = s.recv(65536)
    if not chunk:
        break
    received += len(chunk)
t1 = time.perf_counter()
elapsed = t1 - t0
bps = received / elapsed if elapsed > 0 else 0.0
print(json.dumps({"bytes": received, "elapsed_sec": elapsed, "bytes_per_sec": bps}))
`

// downClientPy classifies the error class a single connection attempt to
// argv[1]:argv[2] gets.
const downClientPy = `
import socket, json, sys
host, port = sys.argv[1], int(sys.argv[2])
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.settimeout(5)
try:
    s.connect((host, port))
    s.sendall(b"ping")
    data = s.recv(64)
    print(json.dumps({"outcome": "connected", "data": data.decode(errors="replace")}))
except ConnectionRefusedError as e:
    print(json.dumps({"outcome": "refused", "error": str(e)}))
except socket.timeout:
    print(json.dumps({"outcome": "timeout"}))
except Exception as e:
    print(json.dumps({"outcome": "other", "error": str(e), "type": type(e).__name__}))
`

// pingerBackgroundPy is meant to be launched with `docker exec -d`: it opens
// one connection to argv[1]:argv[2] and pings it every argv[4]ms for
// argv[5] seconds, appending one JSON line per attempt to argv[3] so the
// host process can apply a fault mid-run and read the log back afterward.
// pingerBackgroundPy distinguishes THREE outcomes per attempt, not two: "ok"
// (a real echoed match), "closed" (the peer half-closed — recv() returns
// b"" with no exception at all, which an earlier version of this script
// miscounted as "ok" since it never checked for an empty read), and
// "timeout"/"reset"/"oserror" for the exception cases. This distinction is
// exactly what caught a real classification bug in this harness: a killed
// container's connection was observed to sometimes end in a graceful
// zero-byte read rather than an exception, which silently passed as
// success until this script started checking the actual bytes.
const pingerBackgroundPy = `
import socket, time, json, sys
host, port, outfile, interval_ms, duration_s = sys.argv[1], int(sys.argv[2]), sys.argv[3], int(sys.argv[4]), float(sys.argv[5])
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.settimeout(0.6)
s.connect((host, port))
end = time.time() + duration_s
with open(outfile, "w") as f:
    while time.time() < end:
        rec = {"t": time.time()}
        try:
            s.sendall(b"ping")
            data = s.recv(64)
            if data == b"":
                rec["outcome"] = "closed"
            elif data == b"ping":
                rec["outcome"] = "ok"
            else:
                rec["outcome"] = "unexpected_data"
                rec["data"] = repr(data)
        except socket.timeout:
            rec["outcome"] = "timeout"
        except ConnectionResetError:
            rec["outcome"] = "reset"
        except BrokenPipeError:
            rec["outcome"] = "reset"
        except OSError as e:
            rec["outcome"] = "oserror"
            rec["error"] = str(e)
        f.write(json.dumps(rec) + "\n")
        f.flush()
        if rec["outcome"] in ("closed", "reset", "oserror"):
            break
        time.sleep(interval_ms / 1000.0)
`
