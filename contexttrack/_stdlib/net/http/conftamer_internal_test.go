package http

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

type conftamerShortWriter struct{ calls int }

func (w *conftamerShortWriter) Write(p []byte) (int, error) {
	w.calls++
	return len(p) - 1, nil
}

type conftamerErrorWriter struct {
	calls int
	err   error
}

func (w *conftamerErrorWriter) Write([]byte) (int, error) {
	w.calls++
	return 0, w.err
}

func conftamerTestLogger(w io.Writer) (*conftamerLogger, *bytes.Buffer) {
	diagnostics := new(bytes.Buffer)
	log := newConftamerLogger(w, "unit", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	log.diagnostics = diagnostics
	return log, diagnostics
}

func TestConftamerLoggerShortWrite(t *testing.T) {
	output := &conftamerShortWriter{}
	log, diagnostics := conftamerTestLogger(output)
	event := conftamerResponseEvent{StatusCode: 200}
	event.Kind = "send_response"
	event.ExchangeID = 1
	for range 2 {
		if err := log.write(&event.conftamerEnvelope, &event); !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("write error = %v, want io.ErrShortWrite", err)
		}
	}
	if output.calls != 1 {
		t.Fatalf("write calls = %d, want 1", output.calls)
	}
	if got := strings.Count(diagnostics.String(), "conftamer: capture failed:"); got != 1 {
		t.Fatalf("failure diagnostics = %d, want 1; output: %q", got, diagnostics.String())
	}
}

func TestConftamerLoggerLatchesWriteFailure(t *testing.T) {
	writeErr := errors.New("disk unavailable")
	output := &conftamerErrorWriter{err: writeErr}
	log, diagnostics := conftamerTestLogger(output)
	event := conftamerResponseEvent{StatusCode: 200}
	event.Kind = "receive_response"
	event.ExchangeID = 9

	for range 2 {
		if err := log.write(&event.conftamerEnvelope, &event); !errors.Is(err, writeErr) {
			t.Fatalf("write error = %v, want %v", err, writeErr)
		}
	}
	if output.calls != 1 {
		t.Fatalf("write calls = %d, want 1", output.calls)
	}
	if got := strings.Count(diagnostics.String(), "disk unavailable"); got != 1 {
		t.Fatalf("write-error diagnostics = %d, want 1; output: %q", got, diagnostics.String())
	}
}

func TestConftamerLoggerConcurrentSequence(t *testing.T) {
	const eventCount = 64
	output := new(bytes.Buffer)
	log, diagnostics := conftamerTestLogger(output)
	start := make(chan struct{})
	errorsSeen := make(chan error, eventCount)
	var workers sync.WaitGroup
	for exchangeID := uint64(1); exchangeID <= eventCount; exchangeID++ {
		workers.Add(1)
		go func(exchangeID uint64) {
			defer workers.Done()
			<-start
			event := conftamerResponseEvent{StatusCode: 200}
			event.Kind = "send_response"
			event.ExchangeID = exchangeID
			errorsSeen <- log.write(&event.conftamerEnvelope, &event)
		}(exchangeID)
	}
	close(start)
	workers.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("concurrent write: %v", err)
		}
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("unexpected diagnostics: %q", diagnostics.String())
	}

	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte{'\n'})
	if len(lines) != eventCount {
		t.Fatalf("record count = %d, want %d", len(lines), eventCount)
	}
	seen := make(map[uint64]bool, eventCount)
	for physicalLine, line := range lines {
		var event conftamerResponseEvent
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("line %d is not a complete JSON record: %v", physicalLine+1, err)
		}
		if event.SchemaVersion != 2 || event.CaptureID != "unit" || event.ProcessID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
			t.Fatalf("line %d has wrong envelope: %+v", physicalLine+1, event.conftamerEnvelope)
		}
		if seen[event.Seq] {
			t.Fatalf("duplicate sequence %d", event.Seq)
		}
		seen[event.Seq] = true
	}
	for seq := uint64(1); seq <= eventCount; seq++ {
		if !seen[seq] {
			t.Fatalf("missing sequence %d", seq)
		}
	}
}

func TestConftamerLoggerRejectsMarshalAndStringFailures(t *testing.T) {
	t.Run("marshal", func(t *testing.T) {
		output := new(bytes.Buffer)
		log, _ := conftamerTestLogger(output)
		header := conftamerEnvelope{ExchangeID: 1, Kind: "send_response"}
		record := struct {
			conftamerEnvelope
			Unsupported chan int `json:"unsupported"`
		}{conftamerEnvelope: header, Unsupported: make(chan int)}

		firstErr := log.write(&record.conftamerEnvelope, &record)
		if firstErr == nil {
			t.Fatal("marshal error = nil, want failure")
		}
		if secondErr := log.write(&record.conftamerEnvelope, &record); secondErr != firstErr {
			t.Fatalf("latched error = %v, want first error %v", secondErr, firstErr)
		}
		if output.Len() != 0 {
			t.Fatalf("output after marshal failure = %q, want empty", output.String())
		}
	})

	t.Run("invalid UTF-8", func(t *testing.T) {
		output := new(bytes.Buffer)
		log, _ := conftamerTestLogger(output)
		event := conftamerRequestEvent{
			Request: conftamerRequestLabel{
				Method: string([]byte{0xff}),
				Host:   stringPointer("example.test"),
				Path:   "/",
			},
		}
		event.Kind = "send_request"
		event.ExchangeID = 1

		if err := log.write(&event.conftamerEnvelope, &event); !errors.Is(err, errConftamerInvalidUTF8) {
			t.Fatalf("write error = %v, want invalid UTF-8", err)
		}
		if output.Len() != 0 {
			t.Fatalf("output after invalid string = %q, want empty", output.String())
		}
	})
}

func TestConftamerLoggerRecordShapes(t *testing.T) {
	output := new(bytes.Buffer)
	log, diagnostics := conftamerTestLogger(output)

	request := conftamerRequestEvent{
		ContextID: nil,
		Request: conftamerRequestLabel{
			Method: "GET",
			Host:   stringPointer("example.test"),
			Path:   "",
		},
		APIID: nil,
	}
	request.Kind = "send_request"
	request.ExchangeID = 1
	response := conftamerResponseEvent{StatusCode: 204}
	response.Kind = "receive_response"
	response.ExchangeID = 1
	metadata := conftamerMetadataEvent{Route: nil, APIID: stringPointer("example.org/api")}
	metadata.Kind = "request_metadata"
	metadata.ExchangeID = 2

	for header, record := range map[*conftamerEnvelope]any{
		&request.conftamerEnvelope:  &request,
		&response.conftamerEnvelope: &response,
		&metadata.conftamerEnvelope: &metadata,
	} {
		if err := log.write(header, record); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("unexpected diagnostics: %q", diagnostics.String())
	}

	for physicalLine, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte{'\n'}) {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(line, &fields); err != nil {
			t.Fatalf("line %d: %v", physicalLine+1, err)
		}
		kind := string(fields["kind"])
		switch kind {
		case `"send_request"`:
			assertConftamerJSONFields(t, fields, "schema_version", "capture_id", "process_id", "seq", "exchange_id", "kind", "context_id", "request", "api_id")
			if string(fields["context_id"]) != "null" || string(fields["api_id"]) != "null" {
				t.Fatalf("request nullable fields were omitted or non-null: %s", line)
			}
		case `"receive_response"`:
			assertConftamerJSONFields(t, fields, "schema_version", "capture_id", "process_id", "seq", "exchange_id", "kind", "status_code")
		case `"request_metadata"`:
			assertConftamerJSONFields(t, fields, "schema_version", "capture_id", "process_id", "seq", "exchange_id", "kind", "route", "api_id")
			if string(fields["route"]) != "null" {
				t.Fatalf("metadata route was omitted or non-null: %s", line)
			}
		default:
			t.Fatalf("line %d has unexpected kind %s", physicalLine+1, kind)
		}
	}
}

func assertConftamerJSONFields(t *testing.T, fields map[string]json.RawMessage, names ...string) {
	t.Helper()
	if len(fields) != len(names) {
		t.Fatalf("field count = %d, want %d: %v", len(fields), len(names), fields)
	}
	for _, name := range names {
		if _, ok := fields[name]; !ok {
			t.Errorf("missing field %q: %v", name, fields)
		}
	}
}

func stringPointer(value string) *string { return &value }
