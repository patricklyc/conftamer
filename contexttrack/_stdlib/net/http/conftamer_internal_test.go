package http

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"
	"sync"
	"testing"
)

type conftamerShortWriter struct{ calls int }

type conftamerResponseWriter struct {
	header Header
	status int
}

func (w *conftamerResponseWriter) Header() Header {
	if w.header == nil {
		w.header = make(Header)
	}
	return w.header
}

func (w *conftamerResponseWriter) Write(data []byte) (int, error) { return len(data), nil }
func (w *conftamerResponseWriter) WriteHeader(status int)         { w.status = status }

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

func TestConftamerFullPattern(t *testing.T) {
	cases := []struct {
		name                               string
		original, prefix, matched, pattern string
		want                               string
		ok                                 bool
	}{
		{name: "method pattern", original: "/api/items/7", prefix: "/api", matched: "/items/7", pattern: "GET /items/{id}", want: "GET /api/items/{id}", ok: true},
		{name: "no inferred prefix", original: "/api/items/7", prefix: "", matched: "/items/7", pattern: "GET /items/{id}"},
		{name: "path-only pattern", original: "/api/items/7", prefix: "/api", matched: "/items/7", pattern: "/items/:id", want: "/api/items/:id", ok: true},
		{name: "nested host pattern", original: "/api/v1/items/7", prefix: "/api/v1", matched: "/items/7", pattern: "GET module-b.test/items/{id}", want: "GET module-b.test/api/v1/items/{id}", ok: true},
		{name: "wrong original", original: "/other/items/7", prefix: "/api", matched: "/items/7", pattern: "GET /items/{id}"},
		{name: "pattern without path", original: "/api/items/7", prefix: "/api", matched: "/items/7", pattern: "GET"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, ok := conftamerFullPattern(test.original, test.prefix, test.matched, test.pattern)
			if got != test.want || ok != test.ok {
				t.Fatalf("conftamerFullPattern(%q, %q, %q, %q) = (%q, %t), want (%q, %t)", test.original, test.prefix, test.matched, test.pattern, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestConftamerRouteMetadataAndPrefixCopies(t *testing.T) {
	output, log := conftamerInstallTestLogger(t)
	original := &Request{
		Method: "GET",
		Host:   "module-b.test",
		URL:    &url.URL{Path: "/api/v1/items/a/b", RawPath: "/api/v1/items/a%2Fb"},
	}
	conftamerAttachServerRequest(original)

	var outerCopy, innerCopy *Request
	handler := StripPrefix("/api", HandlerFunc(func(w ResponseWriter, request *Request) {
		outerCopy = request
		StripPrefix("/v1", HandlerFunc(func(_ ResponseWriter, request *Request) {
			innerCopy = request
			ConftamerLogRouted(request, "go_serve_mux", "GET module-b.test/items/{item}")

			wrongMethod := new(Request)
			*wrongMethod = *request
			wrongMethod.Method = "POST"
			ConftamerLogRouted(wrongMethod, "httprouter", "/items/:item")

			wrongAuthority := new(Request)
			*wrongAuthority = *request
			wrongAuthority.Host = "other.test"
			ConftamerLogRouted(wrongAuthority, "httprouter", "/items/:item")
		})).ServeHTTP(w, request)
	}))
	handler.ServeHTTP(new(conftamerResponseWriter), original)

	if original.conftamerStrippedPrefix != "" || outerCopy == nil || innerCopy == nil {
		t.Fatalf("prefix copies missing or original mutated: original=%q outer=%p inner=%p", original.conftamerStrippedPrefix, outerCopy, innerCopy)
	}
	if outerCopy.conftamerStrippedPrefix != "/api" || innerCopy.conftamerStrippedPrefix != "/api/v1" {
		t.Fatalf("prefix copies = %q/%q, want /api and /api/v1", outerCopy.conftamerStrippedPrefix, innerCopy.conftamerStrippedPrefix)
	}
	if original.URL.Path != "/api/v1/items/a/b" || outerCopy.URL.Path != "/v1/items/a/b" || innerCopy.URL.Path != "/items/a/b" || innerCopy.URL.RawPath != "/items/a%2Fb" {
		t.Fatalf("paths after nested strips = %q/%q/%q raw=%q", original.URL.Path, outerCopy.URL.Path, innerCopy.URL.Path, innerCopy.URL.RawPath)
	}

	metadata := conftamerMetadataRecords(t, output.Bytes())
	if len(metadata) != 3 {
		t.Fatalf("metadata records = %d, want 3", len(metadata))
	}
	first := metadata[0].Route
	if first == nil || first.Dialect != "go_serve_mux" || first.Pattern != "GET module-b.test/items/{item}" || first.MatchedPath != "/items/a/b" || first.FullPattern == nil || *first.FullPattern != "GET module-b.test/api/v1/items/{item}" {
		t.Fatalf("verified route = %+v", first)
	}
	for index, event := range metadata[1:] {
		if event.Route == nil || event.Route.FullPattern != nil {
			t.Fatalf("unverified route %d = %+v, want null full pattern", index+1, event.Route)
		}
	}
	if log.stopped.Load() {
		t.Fatal("valid route metadata stopped the logger")
	}
}

func TestConftamerStripPrefixRejectsRawPathMismatch(t *testing.T) {
	_, _ = conftamerInstallTestLogger(t)
	request := &Request{Method: "GET", URL: &url.URL{Path: "/api/items/7", RawPath: "/wrong/items/7"}}
	conftamerAttachServerRequest(request)
	called := false
	StripPrefix("/api", HandlerFunc(func(ResponseWriter, *Request) { called = true })).ServeHTTP(new(conftamerResponseWriter), request)
	if called || request.conftamerStrippedPrefix != "" {
		t.Fatalf("mismatched RawPath called handler or changed prefix: called=%t prefix=%q", called, request.conftamerStrippedPrefix)
	}
}

func TestConftamerMetadataHelpersIgnoreUntracedRequests(t *testing.T) {
	output, log := conftamerInstallTestLogger(t)
	request := &Request{Method: "GET", URL: &url.URL{Path: "/untraced"}}
	ConftamerSetServerAPI(request, "example.org/server")
	ConftamerLogRouted(request, "httprouter", "/untraced")
	if output.Len() != 0 || log.stopped.Load() {
		t.Fatalf("untraced metadata output/stopped = %q/%t", output.String(), log.stopped.Load())
	}
}

func TestConftamerInvalidMetadataStopsCapture(t *testing.T) {
	cases := []struct {
		name string
		run  func(*Request)
	}{
		{name: "empty client API", run: func(request *Request) { _ = ConftamerWithClientAPI(request, "") }},
		{name: "empty server API", run: func(request *Request) { ConftamerSetServerAPI(request, "") }},
		{name: "invalid API UTF-8", run: func(request *Request) { ConftamerSetServerAPI(request, string([]byte{0xff})) }},
		{name: "unknown dialect", run: func(request *Request) { ConftamerLogRouted(request, "custom", "/items/:id") }},
		{name: "empty pattern", run: func(request *Request) { ConftamerLogRouted(request, "httprouter", "") }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, log := conftamerInstallTestLogger(t)
			request := &Request{Method: "GET", URL: &url.URL{Path: "/items/7"}}
			conftamerAttachServerRequest(request)
			test.run(request)
			if !log.stopped.Load() || !strings.Contains(log.diagnostics.(*bytes.Buffer).String(), "conftamer: capture failed:") {
				t.Fatalf("invalid metadata did not stop capture: stopped=%t diagnostics=%q", log.stopped.Load(), log.diagnostics.(*bytes.Buffer).String())
			}
		})
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

func conftamerMetadataRecords(t *testing.T, content []byte) []conftamerMetadataEvent {
	t.Helper()
	var records []conftamerMetadataEvent
	for physicalLine, line := range bytes.Split(bytes.TrimSpace(content), []byte{'\n'}) {
		var envelope conftamerEnvelope
		if err := json.Unmarshal(line, &envelope); err != nil {
			t.Fatalf("line %d envelope: %v", physicalLine+1, err)
		}
		if envelope.Kind != "request_metadata" {
			continue
		}
		var event conftamerMetadataEvent
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("line %d metadata: %v", physicalLine+1, err)
		}
		records = append(records, event)
	}
	return records
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
