package capture

import (
	"bytes"
	"testing"
)

// spec: R-CLI-9 (proposed)
func TestWriteEntryThenReadCassetteRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	e1 := Entry{Seq: 1, Method: "GET", URL: "/a", Status: 200, Body: "hello", DurationMS: 5}
	e2 := Entry{Seq: 2, Method: "POST", URL: "/b", Status: 201, RespBody: "world", DurationMS: 9}

	if err := WriteEntry(&buf, e1); err != nil {
		t.Fatalf("WriteEntry(e1): %v", err)
	}
	if err := WriteEntry(&buf, e2); err != nil {
		t.Fatalf("WriteEntry(e2): %v", err)
	}

	got, err := ReadCassette(&buf)
	if err != nil {
		t.Fatalf("ReadCassette: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0].Method != "GET" || got[0].Body != "hello" {
		t.Errorf("entry 1 = %+v, want GET/hello", got[0])
	}
	if got[1].Method != "POST" || got[1].RespBody != "world" {
		t.Errorf("entry 2 = %+v, want POST/world", got[1])
	}
}

// spec: R-CLI-9 (proposed)
func TestCassetteIsOneJSONObjectPerLine(t *testing.T) {
	var buf bytes.Buffer
	_ = WriteEntry(&buf, Entry{Seq: 1, Method: "GET", URL: "/a"})
	_ = WriteEntry(&buf, Entry{Seq: 2, Method: "GET", URL: "/b"})

	lines := bytes.Split(bytes.TrimRight(buf.Bytes(), "\n"), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2 (one JSON object per line, for git diff)", len(lines))
	}
}

// spec: R-CLI-9 (proposed)
func TestEntryBodyRoundTripsThroughBase64ForBinary(t *testing.T) {
	raw := []byte{0xff, 0xfe, 0x00, 0x01}
	text, enc := encodeBody(raw)
	e := Entry{Body: text, BodyEncoding: enc}

	got, err := e.RequestBody()
	if err != nil {
		t.Fatalf("RequestBody: %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Errorf("RequestBody() = %v, want %v", got, raw)
	}
}
