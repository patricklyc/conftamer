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

func (w *conftamerShortWriter) Write(data []byte) (int, error) {
	w.calls++
	return len(data) - 1, nil
}

func conftamerTestLogger(output io.Writer) (*conftamerLogger, *bytes.Buffer) {
	diagnostics := new(bytes.Buffer)
	log := newConftamerLogger(output, "unit", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	log.diagnostics = diagnostics
	return log, diagnostics
}

func conftamerTestResponse(exchangeID uint64) conftamerResponseEvent {
	event := conftamerResponseEvent{StatusCode: 200}
	event.Kind = "send_response"
	event.ExchangeID = exchangeID
	return event
}

func TestConftamerLoggerShortWrite(t *testing.T) {
	output := new(conftamerShortWriter)
	log, diagnostics := conftamerTestLogger(output)
	event := conftamerTestResponse(1)
	for range 2 {
		if err := log.write(&event.conftamerEnvelope, &event); !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("write error = %v, want io.ErrShortWrite", err)
		}
	}
	if output.calls != 1 || strings.Count(diagnostics.String(), "conftamer: capture failed:") != 1 {
		t.Fatalf("write calls/diagnostics = %d/%q", output.calls, diagnostics.String())
	}
}

func TestConftamerLoggerConcurrentSequence(t *testing.T) {
	const eventCount = 64
	output := new(bytes.Buffer)
	log, diagnostics := conftamerTestLogger(output)
	errorsSeen := make(chan error, eventCount)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for exchangeID := uint64(1); exchangeID <= eventCount; exchangeID++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			event := conftamerTestResponse(exchangeID)
			errorsSeen <- log.write(&event.conftamerEnvelope, &event)
		}()
	}
	close(start)
	workers.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("unexpected diagnostics: %q", diagnostics.String())
	}
	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte{'\n'})
	if len(lines) != eventCount {
		t.Fatalf("record count = %d, want %d", len(lines), eventCount)
	}
	for index, line := range lines {
		var event conftamerResponseEvent
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("line %d: %v", index+1, err)
		}
		if event.SchemaVersion != 2 || event.CaptureID != "unit" || event.ProcessID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || event.Seq != uint64(index+1) {
			t.Fatalf("line %d envelope = %+v", index+1, event.conftamerEnvelope)
		}
	}
}

func TestConftamerLoggerRejectsInvalidRecords(t *testing.T) {
	t.Run("marshal", func(t *testing.T) {
		output := new(bytes.Buffer)
		log, _ := conftamerTestLogger(output)
		record := struct {
			conftamerEnvelope
			Unsupported chan int `json:"unsupported"`
		}{Unsupported: make(chan int)}
		record.Kind, record.ExchangeID = "send_response", 1
		firstErr := log.write(&record.conftamerEnvelope, &record)
		if firstErr == nil || log.write(&record.conftamerEnvelope, &record) != firstErr || output.Len() != 0 {
			t.Fatalf("marshal/latched error or output = %v/%q", firstErr, output.String())
		}
	})

	t.Run("invalid UTF-8", func(t *testing.T) {
		output := new(bytes.Buffer)
		log, _ := conftamerTestLogger(output)
		event := conftamerRequestEvent{Request: conftamerRequestLabel{
			Method: string([]byte{0xff}), Host: stringPointer("example.test"), Path: "/",
		}}
		event.Kind, event.ExchangeID = "send_request", 1
		if err := log.write(&event.conftamerEnvelope, &event); !errors.Is(err, errConftamerInvalidUTF8) || output.Len() != 0 {
			t.Fatalf("write error/output = %v/%q", err, output.String())
		}
	})
}

func TestConftamerLoggerRecordShapes(t *testing.T) {
	request := conftamerRequestEvent{Request: conftamerRequestLabel{Method: "GET", Host: stringPointer("example.test")}}
	request.Kind, request.ExchangeID = "send_request", 1
	response := conftamerTestResponse(1)
	response.Kind = "receive_response"
	metadata := conftamerMetadataEvent{APIID: stringPointer("example.org/api")}
	metadata.Kind, metadata.ExchangeID = "request_metadata", 2

	output := new(bytes.Buffer)
	log, diagnostics := conftamerTestLogger(output)
	for header, record := range map[*conftamerEnvelope]any{
		&request.conftamerEnvelope: &request, &response.conftamerEnvelope: &response, &metadata.conftamerEnvelope: &metadata,
	} {
		if err := log.write(header, record); err != nil {
			t.Fatal(err)
		}
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("unexpected diagnostics: %q", diagnostics.String())
	}

	wantFields := map[string][]string{
		`"send_request"`:     {"schema_version", "capture_id", "process_id", "seq", "exchange_id", "kind", "context_id", "request", "api_id"},
		`"receive_response"`: {"schema_version", "capture_id", "process_id", "seq", "exchange_id", "kind", "status_code"},
		`"request_metadata"`: {"schema_version", "capture_id", "process_id", "seq", "exchange_id", "kind", "route", "api_id"},
	}
	for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte{'\n'}) {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(line, &fields); err != nil {
			t.Fatal(err)
		}
		kind := string(fields["kind"])
		assertConftamerJSONFields(t, fields, wantFields[kind]...)
		if kind == `"send_request"` && (string(fields["context_id"]) != "null" || string(fields["api_id"]) != "null") || kind == `"request_metadata"` && string(fields["route"]) != "null" {
			t.Fatalf("nullable fields are not explicit nulls: %s", line)
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
			t.Errorf("missing field %q", name)
		}
	}
}

func stringPointer(value string) *string { return &value }
