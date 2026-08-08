package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// lastJSONObject finds the last line of output that parses as a JSON object
// — docker exec's combined output can carry noise ahead of it (e.g. an apk
// install's progress lines) even though the script's own print is always
// the last line it writes.
func lastJSONObject(output string) (map[string]any, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err == nil {
			return m, nil
		}
	}
	return nil, fmt.Errorf("no JSON object line found in output: %q", output)
}

// float64s converts a []any (as decoded from a JSON array of numbers) into
// []float64.
func float64s(v any) []float64 {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]float64, 0, len(arr))
	for _, x := range arr {
		if f, ok := x.(float64); ok {
			out = append(out, f)
		}
	}
	return out
}

// cpuStatUsageUsec parses the "usage_usec <n>" line out of a cgroup v2
// cpu.stat file's raw contents.
func cpuStatUsageUsec(raw string) (int64, error) {
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "usage_usec" {
			return strconv.ParseInt(fields[1], 10, 64)
		}
	}
	return 0, fmt.Errorf("usage_usec not found in cpu.stat: %q", raw)
}
