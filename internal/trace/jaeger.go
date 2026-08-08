package trace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Jaeger reads traces from a Jaeger query service — the one backend
// R-VER-13 names as supported in v0.
//
// The API used is the one Jaeger's own UI uses and the one a developer
// already has published when `jaeger*` appears in their compose file:
//
//	GET /api/services
//	GET /api/traces?service=<name>&start=<µs>&end=<µs>&limit=<n>
//
// Both were exercised against a real jaegertracing/jaeger:2.10.0 with real
// OTLP spans exported into it, and testdata/jaeger_traces.json is that
// server's verbatim response. Two properties of that response are why this
// backend was chosen over the alternatives: one request returns the *whole*
// span tree (parent links included, via `references`), and the service name
// of each span is resolvable from the same document (the per-trace
// `processes` map). A chain therefore needs exactly one round trip and no
// second query per span.
type Jaeger struct {
	BaseURL string
	HTTP    *http.Client
}

func (j *Jaeger) client() *http.Client {
	if j.HTTP != nil {
		return j.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// jaegerTracesResponse is the subset of Jaeger's /api/traces payload this
// package reads. Fields it does not read (logs, warnings, offset, ...) are
// deliberately absent rather than modelled: an unknown field is ignored by
// encoding/json, so a Jaeger release adding one cannot break ingestion.
type jaegerTracesResponse struct {
	Data []struct {
		TraceID   string `json:"traceID"`
		Processes map[string]struct {
			ServiceName string `json:"serviceName"`
		} `json:"processes"`
		Spans []struct {
			TraceID       string `json:"traceID"`
			SpanID        string `json:"spanID"`
			OperationName string `json:"operationName"`
			ProcessID     string `json:"processID"`
			// startTime and duration are microseconds since the epoch and
			// microseconds respectively — Jaeger's own units, confirmed
			// against the captured response.
			StartTime  int64 `json:"startTime"`
			Duration   int64 `json:"duration"`
			References []struct {
				RefType string `json:"refType"`
				SpanID  string `json:"spanID"`
			} `json:"references"`
			Tags []struct {
				Key   string          `json:"key"`
				Value json.RawMessage `json:"value"`
			} `json:"tags"`
		} `json:"spans"`
	} `json:"data"`
	Errors []struct {
		Msg string `json:"msg"`
	} `json:"errors"`
}

// Traces implements Source against Jaeger's query API.
func (j *Jaeger) Traces(ctx context.Context, service string, start, end time.Time, limit int) ([]Trace, error) {
	u := fmt.Sprintf("%s/api/traces?service=%s&start=%d&end=%d&limit=%d",
		strings.TrimRight(j.BaseURL, "/"), url.QueryEscape(service), start.UnixMicro(), end.UnixMicro(), limit)
	body, err := j.get(ctx, u)
	if err != nil {
		return nil, err
	}
	return decodeJaegerTraces(body)
}

// Services lists the service names the backend knows about. Used by Open to
// identify a Jaeger, and useful on its own: a query scoped to a service the
// backend has never heard of returns nothing, which is silence rather than
// an answer.
func (j *Jaeger) Services(ctx context.Context) ([]string, error) {
	body, err := j.get(ctx, strings.TrimRight(j.BaseURL, "/")+"/api/services")
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("not a Jaeger /api/services response: %w", err)
	}
	return resp.Data, nil
}

func (j *Jaeger) get(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := j.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
}

// maxResponseBytes bounds one query's response. A trace backend answering
// with something enormous is a reason to give up on the chain, not a reason
// to consume unbounded memory inside a test harness.
const maxResponseBytes = 64 << 20

// decodeJaegerTraces converts Jaeger's payload into this package's shape.
// Split out from Traces so it can be exercised against the recorded real
// response with no HTTP involved.
func decodeJaegerTraces(body []byte) ([]Trace, error) {
	var resp jaegerTracesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode Jaeger traces: %w", err)
	}
	if len(resp.Errors) > 0 {
		// Jaeger reports partial failures here while still returning 200.
		// Reporting the chain built from a partial read as if it were
		// complete is the silent omission this project rejects.
		return nil, fmt.Errorf("Jaeger reported an error: %s", resp.Errors[0].Msg)
	}
	traces := make([]Trace, 0, len(resp.Data))
	for _, jt := range resp.Data {
		tr := Trace{ID: jt.TraceID}
		for _, js := range jt.Spans {
			s := Span{
				TraceID:   js.TraceID,
				SpanID:    js.SpanID,
				Operation: js.OperationName,
				Service:   jt.Processes[js.ProcessID].ServiceName,
				Start:     time.UnixMicro(js.StartTime),
				Duration:  time.Duration(js.Duration) * time.Microsecond,
			}
			for _, ref := range js.References {
				if ref.RefType == "CHILD_OF" {
					s.ParentID = ref.SpanID
					break
				}
			}
			if len(js.Tags) > 0 {
				s.Attrs = make(map[string]string, len(js.Tags))
				for _, tag := range js.Tags {
					s.Attrs[tag.Key] = tagString(tag.Value)
				}
			}
			tr.Spans = append(tr.Spans, s)
		}
		traces = append(traces, tr)
	}
	return traces, nil
}

// tagString renders a Jaeger tag value as a string. Tags are typed
// (`"type":"int64"` etc.) and the value is the corresponding JSON type, so
// net.peer.port arrives as a number while net.peer.name arrives as a
// string; both have to compare against the "host:port" a fault target is
// written as.
func tagString(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return strconv.FormatBool(b)
	}
	return strings.Trim(string(raw), `"`)
}

// ErrUnsupportedBackend is returned by Open for a reachable endpoint that
// is not a Jaeger query service. It is an error, never a silent nil: a user
// who pointed us at their Tempo must be told that is why no chain appeared.
var ErrUnsupportedBackend = errors.New("unsupported trace backend")

// Open identifies what is listening at baseURL and returns a Source for it,
// or refuses by name (R-VER-13).
//
// The probes are the real, verified distinguishing behaviours of each
// backend, not guesses: a Jaeger query service answers GET /api/services
// with a JSON `data` array (jaegertracing/jaeger:2.10.0); Tempo 404s that
// path and answers GET /api/echo with the literal body `echo`
// (grafana/tempo:2.9.0). Both were confirmed against the real images.
//
// The OpenTelemetry Collector is deliberately not probed for: it has no
// query API at all. A collector in a compose file proves spans are
// exported somewhere, never that they can be read back, so it can only ever
// be refused — and it is refused here as an unidentified endpoint, with the
// same message.
func Open(ctx context.Context, baseURL string, httpc *http.Client) (Source, error) {
	j := &Jaeger{BaseURL: baseURL, HTTP: httpc}
	if _, err := j.Services(ctx); err == nil {
		return j, nil
	}
	if isTempo(ctx, baseURL, j.client()) {
		return nil, fmt.Errorf("%w: %s answers as Grafana Tempo; only Jaeger's query API is implemented (R-VER-13)", ErrUnsupportedBackend, baseURL)
	}
	return nil, fmt.Errorf("%w: %s is not a Jaeger query service (GET /api/services did not answer); only Jaeger is implemented (R-VER-13)", ErrUnsupportedBackend, baseURL)
}

func isTempo(ctx context.Context, baseURL string, httpc *http.Client) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/api/echo", nil)
	if err != nil {
		return false
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	return err == nil && strings.TrimSpace(string(body)) == "echo"
}
