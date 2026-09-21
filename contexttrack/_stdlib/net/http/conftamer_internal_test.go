package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"slices"
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
	event.Sources = []uint64{1}
	return event
}

func TestConftamerLoggerShortWrite(t *testing.T) {
	output := new(conftamerShortWriter)
	log, diagnostics := conftamerTestLogger(output)
	event := conftamerTestResponse(1)
	for range 2 {
		if seq, err := log.write(&event.conftamerEnvelope, &event); seq != 0 || !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("write sequence/error = %d/%v, want 0/io.ErrShortWrite", seq, err)
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
			seq, err := log.write(&event.conftamerEnvelope, &event)
			if err == nil && seq == 0 {
				err = errors.New("successful write returned no sequence")
			}
			errorsSeen <- err
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
		if event.SchemaVersion != 4 || event.CaptureID != "unit" || event.ProcessID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || event.Seq != uint64(index+1) {
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
		firstSeq, firstErr := log.write(&record.conftamerEnvelope, &record)
		secondSeq, secondErr := log.write(&record.conftamerEnvelope, &record)
		if firstSeq != 0 || firstErr == nil || secondSeq != 0 || secondErr != firstErr || output.Len() != 0 {
			t.Fatalf("marshal sequences/latched error or output = %d/%v/%d/%v/%q", firstSeq, firstErr, secondSeq, secondErr, output.String())
		}
	})

	t.Run("invalid UTF-8", func(t *testing.T) {
		output := new(bytes.Buffer)
		log, _ := conftamerTestLogger(output)
		event := conftamerRequestEvent{Request: conftamerRequestLabel{
			Method: string([]byte{0xff}), Host: stringPointer("example.test"), Path: "/",
		}}
		event.Kind, event.ExchangeID = "send_request", 1
		if seq, err := log.write(&event.conftamerEnvelope, &event); seq != 0 || !errors.Is(err, errConftamerInvalidUTF8) || output.Len() != 0 {
			t.Fatalf("write sequence/error/output = %d/%v/%q", seq, err, output.String())
		}
	})
}

func TestConftamerV4RequestShape(t *testing.T) {
	var output bytes.Buffer
	log, _ := conftamerTestLogger(&output)
	event := conftamerRequestEvent{Request: conftamerRequestLabel{
		Method: "GET", Host: stringPointer("example.test"), Path: "/",
	}}
	event.Kind, event.ExchangeID = "send_request", 1
	event.Sources = []uint64{}
	if seq, err := log.write(&event.conftamerEnvelope, &event); seq != 1 || err != nil {
		t.Fatalf("write sequence/error = %d/%v", seq, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(output.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	assertConftamerJSONFields(t, fields, "schema_version", "capture_id",
		"process_id", "seq", "exchange_id", "kind", "sources", "request")
	if string(fields["schema_version"]) != "4" || string(fields["sources"]) != "[]" {
		t.Fatalf("unexpected envelope: %s", output.String())
	}
	if _, exists := fields["context_id"]; exists {
		t.Fatalf("v4 request retained context_id: %s", output.String())
	}
}

func TestConftamerSourceHelpersCopyAndCanonicalize(t *testing.T) {
	_, log := conftamerInstallTestLogger(t)
	ctx := context.WithValue(context.Background(), struct{ name string }{"existing"}, true)
	original := &Request{ctx: ctx}
	requestSource := &Request{conftamerReceiveSequence: 3}
	responseSource := &Response{conftamerReceiveSequence: 1}
	supplied := []ConftamerSource{requestSource, responseSource, requestSource}

	annotated := ConftamerWithSources(original, supplied...)
	supplied[0] = responseSource
	copied := annotated.WithContext(ctx)
	replaced := ConftamerWithSources(copied, responseSource)
	if annotated == original || annotated.Context() != ctx || original.conftamerSources != nil {
		t.Fatalf("annotation changed original or context: annotated=%p original=%p", annotated, original)
	}
	if got := annotated.conftamerSources; !slices.Equal(got, []uint64{1, 3}) {
		t.Fatalf("annotated sources = %v, want [1 3]", got)
	}
	if got := copied.conftamerSources; !slices.Equal(got, []uint64{1, 3}) {
		t.Fatalf("copied sources = %v, want [1 3]", got)
	}
	if got := replaced.conftamerSources; !slices.Equal(got, []uint64{1}) || !slices.Equal(annotated.conftamerSources, []uint64{1, 3}) {
		t.Fatalf("replacement contaminated sources: replaced=%v annotated=%v", got, annotated.conftamerSources)
	}

	log.stopped.Store(true)
	if got := ConftamerWithSources(original, requestSource); got != original {
		t.Fatal("stopped capture changed request identity")
	}
	ConftamerSetReplySources(original, requestSource)
}

func TestConftamerSourceHelpersRejectInvalidAndLateDeclarations(t *testing.T) {
	t.Run("invalid source", func(t *testing.T) {
		_, log := conftamerInstallTestLogger(t)
		request := new(Request)
		if got := ConftamerWithSources(request, new(Response)); got != request || !log.stopped.Load() {
			t.Fatalf("request identity/stopped = %t/%t, want true/true", got == request, log.stopped.Load())
		}
	})

	t.Run("invalid reply target", func(t *testing.T) {
		_, log := conftamerInstallTestLogger(t)
		ConftamerSetReplySources(new(Request))
		if !log.stopped.Load() {
			t.Fatal("invalid reply target did not stop capture")
		}
	})

	t.Run("late reply", func(t *testing.T) {
		_, log := conftamerInstallTestLogger(t)
		slot := new(conftamerReplySources)
		target := &Request{
			conftamerExchange:        &conftamerExchange{id: 1},
			conftamerReceiveSequence: 2,
			conftamerReply:           slot,
		}
		source := &Response{conftamerReceiveSequence: 1}
		ConftamerSetReplySources(target, source)
		if got := slot.freeze(2); !slices.Equal(got, []uint64{1, 2}) {
			t.Fatalf("frozen reply sources = %v, want [1 2]", got)
		}
		ConftamerSetReplySources(target, target)
		if !log.stopped.Load() || !slices.Equal(slot.sources, []uint64{1}) {
			t.Fatalf("late declaration stopped/sources = %t/%v", log.stopped.Load(), slot.sources)
		}
	})
}

func TestConftamerServerAttachmentPreservesContext(t *testing.T) {
	output, _ := conftamerInstallTestLogger(t)
	ctx := context.WithValue(context.Background(), struct{ name string }{"existing"}, true)
	request := &Request{Method: "GET", ProtoMajor: 1, URL: &url.URL{Path: "/"}, ctx: ctx}
	conftamerAttachServerRequest(request)
	if request.Context() != ctx || request.conftamerExchange == nil || request.conftamerReceiveSequence != 1 || request.conftamerReply == nil {
		t.Fatalf("context/exchange/sequence/reply = %t/%v/%d/%v", request.Context() == ctx, request.conftamerExchange, request.conftamerReceiveSequence, request.conftamerReply)
	}
	var event conftamerRequestEvent
	if err := json.Unmarshal(output.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if event.Kind != "receive_request" || event.Seq != request.conftamerReceiveSequence || len(event.Sources) != 0 {
		t.Fatalf("server request event = %+v", event)
	}
}

func TestConftamerServerAttachmentRejectsNonHTTP1(t *testing.T) {
	output, log := conftamerInstallTestLogger(t)
	request := &Request{Method: "GET", ProtoMajor: 2, URL: &url.URL{Path: "/"}}
	conftamerAttachServerRequest(request)
	if request.conftamerExchange != nil || output.Len() != 0 || !log.stopped.Load() ||
		strings.Count(log.diagnostics.(*bytes.Buffer).String(), "capture failed:") != 1 {
		t.Fatalf("exchange=%v output=%q stopped=%t diagnostics=%q", request.conftamerExchange,
			output.String(), log.stopped.Load(), log.diagnostics.(*bytes.Buffer).String())
	}
}

func conftamerInstallTestLogger(t *testing.T) (*bytes.Buffer, *conftamerLogger) {
	t.Helper()
	output := new(bytes.Buffer)
	log, _ := conftamerTestLogger(output)
	previous := conftamerActiveLogger.Swap(log)
	t.Cleanup(func() { conftamerActiveLogger.Store(previous) })
	return output, log
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
