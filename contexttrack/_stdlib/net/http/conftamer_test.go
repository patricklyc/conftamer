package http_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	conftamerCaptureChild     = "CONFTAMER_CAPTURE_TEST_CHILD"
	conftamerH2BoundaryURL    = "CONFTAMER_H2_BOUNDARY_URL"
	conftamerParallelRequests = 12
)

var conftamerConfigurationNames = map[string]bool{
	"CONFTAMER_EVENTS":     true,
	"CONFTAMER_EVENTS_DIR": true,
	"CONFTAMER_CAPTURE_ID": true,
	"GODEBUG":              true,
	conftamerCaptureChild:  true,
	conftamerH2BoundaryURL: true,
}

type conftamerCapturedRecord struct {
	SchemaVersion int      `json:"schema_version"`
	CaptureID     string   `json:"capture_id"`
	ProcessID     string   `json:"process_id"`
	Seq           uint64   `json:"seq"`
	ExchangeID    uint64   `json:"exchange_id"`
	Kind          string   `json:"kind"`
	Sources       []uint64 `json:"sources"`
	Request       struct {
		Path string `json:"path"`
	} `json:"request"`
	StatusCode int `json:"status_code"`
}

type conftamerCountingBody struct {
	reader *strings.Reader
	reads  int
	closes int
}

type conftamerChildProcess struct {
	command *exec.Cmd
	stdout  bytes.Buffer
	stderr  bytes.Buffer
}

type conftamerParallelResult struct {
	index      int
	statusCode int
	protocol   int
	err        error
}

type conftamerResult[T any] struct {
	value T
	err   error
}

func conftamerResultOf[T any](value T, err error) conftamerResult[T] {
	return conftamerResult[T]{value, err}
}
func conftamerMust[T any](t *testing.T, result conftamerResult[T]) T {
	t.Helper()
	if result.err != nil {
		t.Fatal(result.err)
	}
	return result.value
}

func (body *conftamerCountingBody) Read(data []byte) (int, error) {
	body.reads++
	return body.reader.Read(data)
}
func (body *conftamerCountingBody) Close() error {
	body.closes++
	return nil
}

type conftamerFailWriteConn struct {
	net.Conn
	writeCount *atomic.Int32
}

func (conn *conftamerFailWriteConn) Write(data []byte) (int, error) {
	if conn.writeCount.Add(1) == 2 {
		return 1, http.ExportErrServerClosedIdle
	}
	return conn.Conn.Write(data)
}

func TestConftamerCaptureExample(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	conftamerRequest(t, http.DefaultClient, server.URL+"/items/7",
		context.Background(), false, http.StatusOK, 1)
}

func TestConftamerForwardingCaptureExample(t *testing.T) {
	testConftamerForwardingWorkload(t)
}

func TestConftamerParallelAndMultiprocessCaptures(t *testing.T) {
	directory := t.TempDir()
	settings := map[string]string{
		"CONFTAMER_EVENTS_DIR": directory,
		"CONFTAMER_CAPTURE_ID": "parallel-capture",
	}
	enabled := runConftamerChild(t, "parallel", settings, true)
	disabled := runConftamerChild(t, "parallel", nil, false)
	if enabled != disabled {
		t.Fatalf("parallel workload differs with capture enabled:\nenabled: %s\ndisabled: %s", enabled, disabled)
	}
	processes := readConftamerProcessRecords(t, directory)
	if len(processes) != 1 {
		t.Fatalf("parallel process files = %d, want 1", len(processes))
	}
	for processID, records := range processes {
		assertConftamerParallelRecords(t, records, "parallel-capture", processID)
	}

	directory = t.TempDir()
	settings = map[string]string{
		"CONFTAMER_EVENTS_DIR": directory,
		"CONFTAMER_CAPTURE_ID": "multiprocess-capture",
	}
	children := []*conftamerChildProcess{
		newConftamerChildProcess(context.Background(), "parallel", settings),
		newConftamerChildProcess(context.Background(), "parallel", settings),
	}
	for _, child := range children {
		if err := child.command.Start(); err != nil {
			t.Fatal(err)
		}
	}
	for _, child := range children {
		waitConftamerChild(t, child, true)
		if child.stdout.String() != disabled {
			t.Fatalf("multiprocess workload differs with capture enabled: %s", child.stdout.String())
		}
	}
	processes = readConftamerProcessRecords(t, directory)
	if len(processes) != 2 {
		t.Fatalf("shared capture process files = %d, want 2", len(processes))
	}
	for processID, records := range processes {
		assertConftamerParallelRecords(t, records, "multiprocess-capture", processID)
	}
}

func TestConftamerTimeoutRace(t *testing.T) {
	records := captureConftamerMode(t, "timeout")
	if len(records) != 4 {
		t.Fatalf("timeout records = %d, want 4: %+v", len(records), records)
	}
	server := conftamerOnlyRecord(t, records, "receive_request", "/timeout/7", 0)
	response := conftamerOnlyRecord(t, records, "send_response", "", server.ExchangeID)
	if response.StatusCode != http.StatusServiceUnavailable || !slices.Equal(response.Sources, []uint64{server.Seq}) {
		t.Fatalf("timeout response status/sources = %d/%v, want %d/[%d]", response.StatusCode, response.Sources, http.StatusServiceUnavailable, server.Seq)
	}
}

func TestConftamerSourceDeclarations(t *testing.T) {
	records := captureConftamerMode(t, "sources")
	if len(records) != 12 {
		t.Fatalf("source records = %d, want 12: %+v", len(records), records)
	}
	incoming := conftamerOnlyRecord(t, records, "receive_request", "/sources", 0)
	sourceSend := conftamerOnlyRecord(t, records, "send_request", "/backend/source", 0)
	sourceResponse := conftamerOnlyRecord(t, records, "receive_response", "", sourceSend.ExchangeID)
	sinkSend := conftamerOnlyRecord(t, records, "send_request", "/backend/sink", 0)
	sinkResponse := conftamerOnlyRecord(t, records, "receive_response", "", sinkSend.ExchangeID)
	reply := conftamerOnlyRecord(t, records, "send_response", "", incoming.ExchangeID)

	if !slices.Equal(sinkSend.Sources, []uint64{incoming.Seq, sourceResponse.Seq}) {
		t.Fatalf("sink sources = %v, want [%d %d]", sinkSend.Sources, incoming.Seq, sourceResponse.Seq)
	}
	if !slices.Equal(reply.Sources, []uint64{incoming.Seq, sourceResponse.Seq, sinkResponse.Seq}) {
		t.Fatalf("reply sources = %v, want [%d %d %d]", reply.Sources, incoming.Seq, sourceResponse.Seq, sinkResponse.Seq)
	}
	if len(sourceResponse.Sources) != 0 || len(sinkResponse.Sources) != 0 {
		t.Fatalf("received responses inherited sources: %v/%v", sourceResponse.Sources, sinkResponse.Sources)
	}
}

// The pinned v1 222c9b6 --recv-sent baseline displayed 1, 5, 10, 5, 9,
// 17, and 8 edges for simple, forwarding, data, completion, independent,
// fan-in, and repeated. It duplicated client-response edges and omitted the
// first-response-to-second-request edge when the request label repeated.
func TestConftamerPrecisionCases(t *testing.T) {
	t.Run("forwarding", func(t *testing.T) {
		records := captureConftamerPrecisionMode(t, "forwarding", 1)
		if len(records) != 8 {
			t.Fatalf("forwarding records = %d, want 8: %+v", len(records), records)
		}
		assertConftamerForwardingRecords(t, records,
			"/forwarding/incoming", "/backend/downstream", http.StatusNoContent)
	})

	t.Run("data influence", func(t *testing.T) {
		records := captureConftamerPrecisionMode(t, "precision-data", 2)
		if len(records) != 16 {
			t.Fatalf("data records = %d, want 16: %+v", len(records), records)
		}
		assertConftamerForwardingRecords(t, records,
			"/precision/data/allow", "/backend/data/allow", http.StatusAccepted)
		assertConftamerForwardingRecords(t, records,
			"/precision/data/deny", "/backend/data/deny", http.StatusConflict)
	})

	t.Run("required completion", func(t *testing.T) {
		records := captureConftamerPrecisionMode(t, "precision-completion", 1)
		if len(records) != 8 {
			t.Fatalf("completion records = %d, want 8: %+v", len(records), records)
		}
		assertConftamerForwardingRecords(t, records,
			"/precision/completion", "/backend/completion", http.StatusAccepted)
	})

	t.Run("independent branches", func(t *testing.T) {
		records := captureConftamerPrecisionMode(t, "precision-independent", 1)
		if len(records) != 12 {
			t.Fatalf("independent records = %d, want 12: %+v", len(records), records)
		}
		incoming := conftamerOnlyRecord(t, records, "receive_request", "/precision/independent", 0)
		var responses []conftamerCapturedRecord
		for _, branch := range []string{"left", "right"} {
			path := "/backend/independent/" + branch
			send := conftamerOnlyRecord(t, records, "send_request", path, 0)
			response := conftamerOnlyRecord(t, records, "receive_response", "", send.ExchangeID)
			if !slices.Equal(send.Sources, []uint64{incoming.Seq}) {
				t.Fatalf("%s sources = %v, want only [%d]", branch, send.Sources, incoming.Seq)
			}
			responses = append(responses, response)
		}
		reply := conftamerOnlyRecord(t, records, "send_response", "", incoming.ExchangeID)
		want := []uint64{incoming.Seq, responses[0].Seq, responses[1].Seq}
		slices.Sort(want)
		if !slices.Equal(reply.Sources, want) {
			t.Fatalf("independent reply sources = %v, want %v", reply.Sources, want)
		}
	})

	t.Run("fan-in", func(t *testing.T) {
		records := captureConftamerPrecisionMode(t, "precision-fanin", 1)
		if len(records) != 16 {
			t.Fatalf("fan-in records = %d, want 16: %+v", len(records), records)
		}
		incoming := conftamerOnlyRecord(t, records, "receive_request", "/precision/fanin", 0)
		wantSinkSources := []uint64{incoming.Seq}
		for _, branch := range []string{"left", "right"} {
			send := conftamerOnlyRecord(t, records, "send_request", "/backend/fanin/"+branch, 0)
			if !slices.Equal(send.Sources, []uint64{incoming.Seq}) {
				t.Fatalf("fan-in %s sources = %v, want only [%d]", branch, send.Sources, incoming.Seq)
			}
			response := conftamerOnlyRecord(t, records, "receive_response", "", send.ExchangeID)
			wantSinkSources = append(wantSinkSources, response.Seq)
		}
		slices.Sort(wantSinkSources)
		sink := conftamerOnlyRecord(t, records, "send_request", "/backend/fanin/sink", 0)
		if !slices.Equal(sink.Sources, wantSinkSources) {
			t.Fatalf("fan-in sink sources = %v, want %v", sink.Sources, wantSinkSources)
		}
		sinkResponse := conftamerOnlyRecord(t, records, "receive_response", "", sink.ExchangeID)
		reply := conftamerOnlyRecord(t, records, "send_response", "", incoming.ExchangeID)
		if !slices.Equal(reply.Sources, []uint64{incoming.Seq, sinkResponse.Seq}) {
			t.Fatalf("fan-in reply sources = %v, want [%d %d]", reply.Sources, incoming.Seq, sinkResponse.Seq)
		}
	})

	t.Run("repeated request", func(t *testing.T) {
		records := captureConftamerPrecisionMode(t, "precision-repeated", 1)
		if len(records) != 12 {
			t.Fatalf("repeated records = %d, want 12: %+v", len(records), records)
		}
		incoming := conftamerOnlyRecord(t, records, "receive_request", "/precision/repeated", 0)
		sends := conftamerRecords(records, "send_request", "/backend/repeated")
		if len(sends) != 2 || !slices.Equal(sends[0].Sources, []uint64{incoming.Seq}) {
			t.Fatalf("repeated sends = %+v", sends)
		}
		firstResponse := conftamerOnlyRecord(t, records, "receive_response", "", sends[0].ExchangeID)
		if !slices.Equal(sends[1].Sources, []uint64{incoming.Seq, firstResponse.Seq}) || firstResponse.Seq >= sends[1].Seq {
			t.Fatalf("second repeated sources/order = %v/%d >= %d", sends[1].Sources, firstResponse.Seq, sends[1].Seq)
		}
		secondResponse := conftamerOnlyRecord(t, records, "receive_response", "", sends[1].ExchangeID)
		reply := conftamerOnlyRecord(t, records, "send_response", "", incoming.ExchangeID)
		want := []uint64{incoming.Seq, firstResponse.Seq, secondResponse.Seq}
		if !slices.Equal(reply.Sources, want) {
			t.Fatalf("repeated reply sources = %v, want %v", reply.Sources, want)
		}
	})
}

func captureConftamerPrecisionMode(t *testing.T, mode string, wantSendsWithoutSources int) []conftamerCapturedRecord {
	t.Helper()
	records, enabled, _ := captureConftamerModeOutput(t, mode)
	disabled := runConftamerChild(t, mode, nil, false)
	if enabled != disabled {
		t.Fatalf("%s outcome differs with capture enabled:\nenabled: %s\ndisabled: %s", mode, enabled, disabled)
	}
	assertConftamerSourceIntegrity(t, records, wantSendsWithoutSources)
	return records
}

func assertConftamerSourceIntegrity(t *testing.T, records []conftamerCapturedRecord, wantSendsWithoutSources int) {
	t.Helper()
	type serverRequest struct {
		seq  uint64
		path string
	}
	serverRequests := make(map[uint64]serverRequest)
	receives := make(map[uint64]bool)
	sendsWithoutSources := 0
	for _, record := range records {
		if !slices.IsSorted(record.Sources) {
			t.Fatalf("record %d sources are not sorted: %v", record.Seq, record.Sources)
		}
		for index, source := range record.Sources {
			if !receives[source] || source >= record.Seq {
				t.Fatalf("record %d source %d is not a preceding receive", record.Seq, source)
			}
			if index > 0 && source == record.Sources[index-1] {
				t.Fatalf("record %d has duplicate source %d", record.Seq, source)
			}
		}
		if strings.HasPrefix(record.Kind, "receive_") {
			if len(record.Sources) != 0 {
				t.Fatalf("receive record %d has sources %v", record.Seq, record.Sources)
			}
			receives[record.Seq] = true
		}
		if record.Kind == "receive_request" {
			serverRequests[record.ExchangeID] = serverRequest{record.Seq, record.Request.Path}
		}
		if strings.HasPrefix(record.Kind, "send_") && len(record.Sources) == 0 {
			sendsWithoutSources++
		}
		if record.Kind == "send_response" {
			request := serverRequests[record.ExchangeID]
			if !slices.Contains(record.Sources, request.seq) {
				t.Fatalf("reply record %d sources %v omit request %d", record.Seq, record.Sources, request.seq)
			}
			if strings.HasPrefix(request.path, "/backend/") && !slices.Equal(record.Sources, []uint64{request.seq}) {
				t.Fatalf("backend reply record %d has unexpected sources %v", record.Seq, record.Sources)
			}
		}
	}
	if sendsWithoutSources != wantSendsWithoutSources {
		t.Fatalf("sends without sources = %d, want %d", sendsWithoutSources, wantSendsWithoutSources)
	}
}

func assertConftamerForwardingRecords(t *testing.T, records []conftamerCapturedRecord, incomingPath, outboundPath string, wantStatus int) {
	t.Helper()
	incoming := conftamerOnlyRecord(t, records, "receive_request", incomingPath, 0)
	outbound := conftamerOnlyRecord(t, records, "send_request", outboundPath, 0)
	backendIncoming := conftamerOnlyRecord(t, records, "receive_request", outboundPath, 0)
	backendReply := conftamerOnlyRecord(t, records, "send_response", "", backendIncoming.ExchangeID)
	downstream := conftamerOnlyRecord(t, records, "receive_response", "", outbound.ExchangeID)
	reply := conftamerOnlyRecord(t, records, "send_response", "", incoming.ExchangeID)

	if !slices.Equal(outbound.Sources, []uint64{incoming.Seq}) {
		t.Fatalf("%s request sources = %v, want [%d]", outboundPath, outbound.Sources, incoming.Seq)
	}
	if !slices.Equal(backendReply.Sources, []uint64{backendIncoming.Seq}) {
		t.Fatalf("%s backend reply sources = %v, want [%d]", outboundPath, backendReply.Sources, backendIncoming.Seq)
	}
	if !slices.Equal(reply.Sources, []uint64{incoming.Seq, downstream.Seq}) {
		t.Fatalf("%s reply sources = %v, want [%d %d]", incomingPath, reply.Sources, incoming.Seq, downstream.Seq)
	}
	if backendReply.StatusCode != wantStatus || downstream.StatusCode != wantStatus || reply.StatusCode != wantStatus {
		t.Fatalf("%s response statuses = %d/%d/%d, want %d", incomingPath,
			backendReply.StatusCode, downstream.StatusCode, reply.StatusCode, wantStatus)
	}
}

func TestConftamerReplySourceCutoff(t *testing.T) {
	t.Run("informational header does not freeze", func(t *testing.T) {
		records, stderr := captureConftamerModeResult(t, "informational-sources")
		if strings.Contains(stderr, "capture failed:") || len(records) != 4 {
			t.Fatalf("records/stderr = %v/%q", records, stderr)
		}
		request := conftamerOnlyRecord(t, records, "receive_request", "/informational-sources", 0)
		reply := conftamerOnlyRecord(t, records, "send_response", "", request.ExchangeID)
		if !slices.Equal(reply.Sources, []uint64{request.Seq}) {
			t.Fatalf("reply sources = %v, want [%d]", reply.Sources, request.Seq)
		}
	})

	for _, mode := range []string{"late-sources", "timeout-late-sources"} {
		t.Run(mode, func(t *testing.T) {
			records, stderr := captureConftamerModeResult(t, mode)
			if strings.Count(stderr, "capture failed:") != 1 || !strings.Contains(stderr, "reply sources declared after final headers") {
				t.Fatalf("late declaration diagnostics = %q", stderr)
			}
			if len(conftamerRecords(records, "send_response", "")) != 1 {
				t.Fatalf("late declaration records = %+v", records)
			}
		})
	}
}

func TestConftamerHTTP2Boundary(t *testing.T) {
	http.CondSkipHTTP2(t)
	for _, side := range []string{"client", "server"} {
		t.Run(side, func(t *testing.T) {
			records, stderr := runConftamerH2Boundary(t, side)
			if len(records) != 0 || strings.Count(stderr, "capture failed:") != 1 ||
				!strings.Contains(stderr, "non-HTTP/1") {
				t.Fatalf("records=%v stderr=%q", records, stderr)
			}
		})
	}
}

func runConftamerH2Boundary(t *testing.T, side string) ([]conftamerCapturedRecord, string) {
	t.Helper()
	directory := t.TempDir()
	settings := map[string]string{"CONFTAMER_EVENTS_DIR": directory, "CONFTAMER_CAPTURE_ID": "h2-boundary-" + side}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if side == "client" {
		server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		server.EnableHTTP2 = true
		server.StartTLS()
		defer server.Close()
		settings[conftamerH2BoundaryURL] = server.URL

		child := newConftamerChildProcess(ctx, "h2-boundary-client", settings)
		if err := child.command.Run(); err != nil {
			t.Fatalf("HTTP/2 client child failed: %v\nstdout: %s\nstderr: %s", err, child.stdout.String(), child.stderr.String())
		}
		waitConftamerChild(t, child, true)
		return readConftamerRecords(t, directory), child.stderr.String()
	}
	child := newConftamerChildProcess(ctx, "h2-boundary-server", settings)
	stdin := conftamerMust(t, conftamerResultOf(child.command.StdinPipe()))
	stdout := conftamerMust(t, conftamerResultOf(child.command.StdoutPipe()))
	if err := child.command.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatalf("HTTP/2 server did not report its URL: %v", scanner.Err())
	}
	conftamerRequest(t, conftamerH2TestClient(t), scanner.Text(), context.Background(), false, http.StatusOK, 2)
	if _, err := io.WriteString(stdin, "stop\n"); err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	if err := child.command.Wait(); err != nil {
		t.Fatalf("HTTP/2 server child failed: %v\nstderr: %s", err, child.stderr.String())
	}
	waitConftamerChild(t, child, true)
	return readConftamerRecords(t, directory), child.stderr.String()
}

func newConftamerChildProcess(ctx context.Context, mode string, settings map[string]string) *conftamerChildProcess {
	overrides := map[string]string{conftamerCaptureChild: mode}
	for name, value := range settings {
		overrides[name] = value
	}
	child := new(conftamerChildProcess)
	child.command = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestConftamerCaptureChild$", "-test.count=1")
	child.command.Env = conftamerEnvironment(overrides)
	if mode != "h2-boundary-server" {
		child.command.Stdout = &child.stdout
	}
	child.command.Stderr = &child.stderr
	return child
}

func conftamerH2TestClient(t *testing.T) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ForceAttemptHTTP2 = true
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport}
}

func TestConftamerExchangeOwnership(t *testing.T) {
	for _, test := range []struct{ name, mode string }{
		{name: "HTTP/1", mode: "http1"},
		{name: "direct RoundTrip", mode: "direct"},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertConftamerExchange(t, captureConftamerMode(t, test.mode))
		})
	}
}

func TestConftamerDisabledHelpersAreNoOps(t *testing.T) {
	ctx := context.WithValue(context.Background(), struct{ name string }{"existing"}, true)
	request := conftamerMust(t, conftamerResultOf(http.NewRequestWithContext(ctx, http.MethodGet, "http://example.test/", nil)))
	if annotated := http.ConftamerWithSources(request, request); annotated != request || annotated.Context() != ctx {
		t.Fatal("disabled request annotation changed request or context identity")
	}
	http.ConftamerSetReplySources(request, request)
}

func TestConftamerAttemptLifetimes(t *testing.T) {
	if records := captureConftamerMode(t, "invalid"); len(records) != 0 {
		t.Fatalf("invalid request records = %+v, want none", records)
	}

	t.Run("server request copied outbound", func(t *testing.T) {
		records := captureConftamerMode(t, "forwarding")
		requests := conftamerRecords(records, "", "request")
		if len(records) != 8 || len(requests) != 4 {
			t.Fatalf("records/requests = %d/%d, want 8/4", len(records), len(requests))
		}
		seen := make(map[uint64]bool)
		for _, record := range requests {
			seen[record.ExchangeID] = true
		}
		outer := conftamerOnlyRecord(t, records, "receive_request", "/forwarding/incoming", 0)
		inner := conftamerOnlyRecord(t, records, "send_request", "/backend/downstream", 0)
		if len(seen) != 4 || len(outer.Sources) != 0 || !slices.Equal(inner.Sources, []uint64{outer.Seq}) {
			t.Fatalf("request exchanges/sources = %v/%v/%v", seen, outer.Sources, inner.Sources)
		}
	})

	t.Run("redirect", func(t *testing.T) {
		records := captureConftamerMode(t, "redirect")
		sourceSend := conftamerOnlyRecord(t, records, "send_request", "/redirect/source", 0)
		sourceResponse := conftamerOnlyRecord(t, records, "receive_response", "", sourceSend.ExchangeID)
		start := conftamerOnlyRecord(t, records, "send_request", "/redirect/start", 0)
		redirectResponse := conftamerOnlyRecord(t, records, "receive_response", "", start.ExchangeID)
		final := conftamerOnlyRecord(t, records, "send_request", "/redirect/final", 0)
		assertConftamerAttempts(t, records, []conftamerCapturedRecord{start, final}, 2)
		if !slices.Equal(start.Sources, []uint64{sourceResponse.Seq}) || !slices.Equal(final.Sources, []uint64{redirectResponse.Seq}) {
			t.Fatalf("redirect sources = start %v final %v", start.Sources, final.Sources)
		}
	})

	t.Run("retry", func(t *testing.T) {
		records := captureConftamerMode(t, "retry")
		attempts := conftamerRecords(records, "send_request", "/retry/2")
		assertConftamerAttempts(t, records, attempts, 1)
		sourceSend := conftamerOnlyRecord(t, records, "send_request", "/retry/source", 0)
		sourceResponse := conftamerOnlyRecord(t, records, "receive_response", "", sourceSend.ExchangeID)
		for _, attempt := range attempts {
			if !slices.Equal(attempt.Sources, []uint64{sourceResponse.Seq}) {
				t.Fatalf("retry attempt sources = %v, want [%d]", attempt.Sources, sourceResponse.Seq)
			}
		}
		if got := len(conftamerRecords(records, "receive_request", "/retry/2")); got != 1 {
			t.Fatalf("retried server requests = %d, want 1", got)
		}
	})
}

func TestConftamerResponseLifecycles(t *testing.T) {
	want := map[string]int{
		"/empty": 200, "/explicit": 200, "/informational": 200,
		"/implicit": 200, "/repeated": 202, "/switch": 101,
	}
	assertConftamerLifecycles(t, captureConftamerMode(t, "lifecycle"), want)
}

func TestConftamerHTTPBehaviorPreserved(t *testing.T) {
	for _, mode := range []string{"behavior-data", "behavior-cancel"} {
		t.Run(mode, func(t *testing.T) {
			runConftamerChild(t, mode, nil, false)
			if records := captureConftamerMode(t, mode); len(records) == 0 {
				t.Fatal("enabled preservation workload emitted no records")
			}
		})
	}
}

func assertConftamerExchange(t *testing.T, records []conftamerCapturedRecord) {
	t.Helper()
	if len(records) != 4 {
		t.Fatalf("record count = %d, want 4: %+v", len(records), records)
	}
	wantKinds := []string{"send_request", "receive_request", "send_response", "receive_response"}
	for index, want := range wantKinds {
		if records[index].Kind != want {
			t.Fatalf("record %d kind = %q, want %q", index, records[index].Kind, want)
		}
	}
	client, server := records[0], records[1]
	if client.ExchangeID == server.ExchangeID || records[2].ExchangeID != server.ExchangeID || records[3].ExchangeID != client.ExchangeID {
		t.Fatalf("exchange IDs = %d,%d,%d,%d", client.ExchangeID, server.ExchangeID, records[2].ExchangeID, records[3].ExchangeID)
	}
	if len(client.Sources) != 0 || len(server.Sources) != 0 ||
		!slices.Equal(records[2].Sources, []uint64{server.Seq}) || len(records[3].Sources) != 0 {
		t.Fatalf("exchange sources = %v,%v,%v,%v", client.Sources, server.Sources, records[2].Sources, records[3].Sources)
	}
}

func assertConftamerAttempts(t *testing.T, records, attempts []conftamerCapturedRecord, wantResponses int) {
	t.Helper()
	if len(attempts) != 2 || attempts[0].ExchangeID == attempts[1].ExchangeID {
		t.Fatalf("attempts = %+v, want two distinct exchanges", attempts)
	}
	responseExchanges := make(map[uint64]bool)
	for _, record := range records {
		if record.Kind == "receive_response" {
			responseExchanges[record.ExchangeID] = true
		}
	}
	responses := 0
	for _, attempt := range attempts {
		if responseExchanges[attempt.ExchangeID] {
			responses++
		}
	}
	if responses != wantResponses {
		t.Fatalf("attempt responses = %d, want %d", responses, wantResponses)
	}
}

func assertConftamerLifecycles(t *testing.T, records []conftamerCapturedRecord, want map[string]int) {
	t.Helper()
	paths := make(map[uint64]string)
	responses := make(map[string][]int)
	for _, record := range records {
		if strings.HasSuffix(record.Kind, "request") {
			paths[record.ExchangeID] = record.Request.Path
		} else if path := paths[record.ExchangeID]; path != "" {
			responses[path+"/"+record.Kind] = append(responses[path+"/"+record.Kind], record.StatusCode)
		}
	}
	for path, status := range want {
		if len(conftamerRecords(records, "", path)) != 2 {
			t.Errorf("%s request origins != 2", path)
		}
		for _, kind := range []string{"send_response", "receive_response"} {
			got := responses[path+"/"+kind]
			if len(got) != 1 || got[0] != status {
				t.Errorf("%s %s statuses = %v, want [%d]", path, kind, got, status)
			}
		}
	}
	if len(conftamerRecords(records, "", "/panic")) != 2 || len(responses["/panic/send_response"])+len(responses["/panic/receive_response"]) != 0 {
		t.Error("panic must have two origins and no response")
	}
}

func conftamerRecords(records []conftamerCapturedRecord, kind, selector string) []conftamerCapturedRecord {
	var matches []conftamerCapturedRecord
	for _, record := range records {
		kindMatch := kind == "" || record.Kind == kind
		selectorMatch := selector == "request" && strings.HasSuffix(record.Kind, "request") || selector != "request" && strings.Contains(record.Request.Path, selector)
		if kindMatch && selectorMatch {
			matches = append(matches, record)
		}
	}
	return matches
}

func conftamerOnlyRecord(t *testing.T, records []conftamerCapturedRecord, kind, path string, exchangeID uint64) conftamerCapturedRecord {
	t.Helper()
	var matches []conftamerCapturedRecord
	for _, record := range records {
		pathMatch := exchangeID == 0 && strings.Contains(record.Request.Path, path)
		if record.Kind == kind && (pathMatch || record.ExchangeID == exchangeID) {
			matches = append(matches, record)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("%s selector %q/%d records = %d, want 1", kind, path, exchangeID, len(matches))
	}
	return matches[0]
}

func TestConftamerCaptureChild(t *testing.T) {
	mode := os.Getenv(conftamerCaptureChild)
	if mode == "" {
		t.Skip("helper process")
	}
	switch mode {
	case "invalid":
		request := conftamerMust(t, conftamerResultOf(http.NewRequest(http.MethodGet, "http://example.test/invalid", nil)))
		request.URL.Host = ""
		response, err := http.DefaultTransport.RoundTrip(request)
		if response != nil {
			response.Body.Close()
			t.Fatalf("invalid request response = %+v", response)
		}
		if err == nil || err.Error() != "http: no Host in request URL" {
			t.Fatalf("invalid request error = %v", err)
		}
		return
	case "h2-boundary-client":
		target := os.Getenv(conftamerH2BoundaryURL)
		if target == "" {
			t.Fatal("HTTP/2 boundary URL is unset")
		}
		conftamerRequest(t, conftamerH2TestClient(t), target, context.Background(), false, http.StatusOK, 2)
		return
	case "h2-boundary-server":
		testConftamerH2BoundaryServer(t)
		return
	}

	if mode == "forwarding" {
		testConftamerForwardingWorkload(t)
		return
	}
	if mode == "precision-data" {
		testConftamerDataInfluence(t)
		return
	}
	if mode == "precision-completion" {
		testConftamerRequiredCompletion(t)
		return
	}
	if mode == "precision-independent" {
		testConftamerIndependentBranches(t)
		return
	}
	if mode == "precision-fanin" {
		testConftamerFanIn(t)
		return
	}
	if mode == "precision-repeated" {
		testConftamerRepeatedRequest(t)
		return
	}

	started := make(chan struct{}, 1)
	timeoutRelease := make(chan struct{})
	timeoutFinished := make(chan struct{})
	var backendURL string
	if mode == "sources" {
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
		defer backend.Close()
		backendURL = backend.URL
	}
	var handler http.Handler
	if mode == "timeout" || mode == "timeout-late-sources" {
		handler = conftamerTimeoutHandler(t, started, timeoutRelease, timeoutFinished, mode == "timeout-late-sources")
	} else {
		handler = conftamerTestHandler(t, backendURL, started)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	client := http.DefaultClient

	switch mode {
	case "behavior-data":
		testConftamerBodyAndTrailers(t, client, server.URL)
	case "behavior-cancel":
		testConftamerCancellation(t, server.URL, started)
	case "redirect":
		testConftamerRedirect(t, server.URL)
	case "retry":
		testConftamerRetry(t, server.URL)
	case "parallel":
		testConftamerParallelWorkload(t, client, server.URL)
	case "timeout", "timeout-late-sources":
		testConftamerTimeoutWorkload(t, client, server.URL, started, timeoutRelease, timeoutFinished)
	case "sources":
		conftamerRequest(t, client, server.URL+"/sources", context.Background(), false, http.StatusNoContent, 1)
	case "informational-sources":
		conftamerRequest(t, client, server.URL+"/informational-sources", context.Background(), false, http.StatusNoContent, 1)
	case "late-sources":
		conftamerRequest(t, client, server.URL+"/late-sources", context.Background(), false, http.StatusNoContent, 1)
	case "lifecycle":
		statuses := map[string]int{
			"/empty": 200, "/explicit": 200, "/informational": 200,
			"/implicit": 200, "/repeated": 202, "/switch": 101,
		}
		for _, path := range []string{"/empty", "/explicit", "/informational", "/implicit", "/repeated", "/switch"} {
			conftamerRequest(t, client, server.URL+path, context.Background(), false, statuses[path], 1)
		}
		request := conftamerMust(t, conftamerResultOf(http.NewRequestWithContext(
			context.Background(), http.MethodGet, server.URL+"/panic", nil,
		)))
		if response, err := client.Do(request); err == nil {
			response.Body.Close()
			t.Fatal("panic request error = nil")
		}
	default:
		ctx := context.WithValue(context.Background(), struct{ name string }{"derived"}, true)
		conftamerRequest(t, client, server.URL+"/resource", ctx, mode == "direct", http.StatusNoContent, 1)
	}
}

func testConftamerH2BoundaryServer(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	fmt.Fprintln(os.Stdout, server.URL)

	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		t.Fatal("HTTP/2 server release was not received")
	}
}

func conftamerTestHandler(t *testing.T, backendURL string, started chan<- struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/implicit":
			_, _ = io.WriteString(w, "body")
		case "/empty":
		case "/explicit":
			w.WriteHeader(http.StatusOK)
		case "/informational":
			w.WriteHeader(http.StatusEarlyHints)
			w.WriteHeader(http.StatusOK)
		case "/informational-sources":
			w.WriteHeader(http.StatusEarlyHints)
			http.ConftamerSetReplySources(request, request)
			w.WriteHeader(http.StatusNoContent)
		case "/late-sources":
			w.WriteHeader(http.StatusNoContent)
			http.ConftamerSetReplySources(request, request)
		case "/repeated":
			w.WriteHeader(http.StatusAccepted)
			w.WriteHeader(http.StatusInternalServerError)
		case "/switch":
			w.Header().Set("Connection", "Upgrade")
			w.Header().Set("Upgrade", "conftamer-test")
			w.WriteHeader(http.StatusSwitchingProtocols)
		case "/panic":
			panic(http.ErrAbortHandler)
		case "/redirect/start":
			http.Redirect(w, request, "/redirect/final", http.StatusFound)
		case "/behavior/data":
			data, err := io.ReadAll(request.Body)
			if err != nil {
				t.Error(err)
				return
			}
			w.Header().Set("Trailer", "X-Conftamer-Trailer")
			w.(http.Flusher).Flush()
			_, _ = w.Write(data)
			w.Header().Set("X-Conftamer-Trailer", "complete")
		case "/behavior/cancel":
			started <- struct{}{}
			<-request.Context().Done()
		case "/sources":
			conftamerSourceWorkload(t, w, request, backendURL)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
}

func testConftamerForwardingWorkload(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer backend.Close()
	frontend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, incoming *http.Request) {
		conftamerForward(t, w, incoming, backend.URL+"/backend/downstream")
	}))
	defer frontend.Close()

	conftamerRequest(t, http.DefaultClient, frontend.URL+"/forwarding/incoming",
		context.Background(), false, http.StatusNoContent, 1)
	fmt.Fprintln(os.Stdout, "conftamer-workload: forwarding:204")
}

func testConftamerDataInfluence(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		status := map[string]int{"allow": http.StatusAccepted, "deny": http.StatusConflict}
		w.WriteHeader(status[strings.TrimPrefix(request.URL.Path, "/backend/data/")])
	}))
	defer backend.Close()
	frontend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, incoming *http.Request) {
		choice := strings.TrimPrefix(incoming.URL.Path, "/precision/data/")
		conftamerForward(t, w, incoming, backend.URL+"/backend/data/"+choice)
	}))
	defer frontend.Close()

	for choice, status := range map[string]int{"allow": http.StatusAccepted, "deny": http.StatusConflict} {
		conftamerRequest(t, http.DefaultClient, frontend.URL+"/precision/data/"+choice,
			context.Background(), false, status, 1)
	}
	fmt.Fprintln(os.Stdout, "conftamer-workload: data:202,409")
}

func testConftamerRequiredCompletion(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusAccepted)
	}))
	defer backend.Close()
	frontend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, incoming *http.Request) {
		conftamerForward(t, w, incoming, backend.URL+"/backend/completion")
	}))
	defer frontend.Close()

	outcomes := make(chan conftamerResult[int], 1)
	go func() {
		response, err := http.Get(frontend.URL + "/precision/completion")
		status := 0
		if response != nil {
			status = response.StatusCode
			err = errors.Join(err, response.Body.Close())
		}
		outcomes <- conftamerResultOf(status, err)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("completion backend did not start")
	}
	select {
	case outcome := <-outcomes:
		t.Fatalf("reply completed before required response: %+v", outcome)
	default:
	}
	close(release)
	select {
	case outcome := <-outcomes:
		if outcome.err != nil || outcome.value != http.StatusAccepted {
			t.Fatalf("completion outcome = %+v, want status 202", outcome)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("completion reply did not finish")
	}
	fmt.Fprintln(os.Stdout, "conftamer-workload: completion:202")
}

func testConftamerIndependentBranches(t *testing.T) {
	started := make(chan string, 2)
	releases := map[string]chan struct{}{"left": make(chan struct{}), "right": make(chan struct{})}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		branch := strings.TrimPrefix(request.URL.Path, "/backend/independent/")
		started <- branch
		<-releases[branch]
		w.WriteHeader(http.StatusNoContent)
	}))
	defer backend.Close()
	frontend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, incoming *http.Request) {
		responses, err := conftamerFetchAll(incoming, []string{
			backend.URL + "/backend/independent/left",
			backend.URL + "/backend/independent/right",
		})
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer func() {
			for _, response := range responses {
				response.Body.Close()
			}
		}()
		sources := []http.ConftamerSource{responses[0], responses[1]}
		http.ConftamerSetReplySources(incoming, sources...)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer frontend.Close()

	outcomes := make(chan conftamerResult[int], 1)
	go func() { outcomes <- conftamerGetStatus(frontend.URL + "/precision/independent") }()
	seen := make(map[string]bool)
	for range 2 {
		select {
		case branch := <-started:
			seen[branch] = true
		case <-time.After(5 * time.Second):
			t.Fatal("independent branch did not start")
		}
	}
	if len(seen) != 2 {
		t.Fatalf("started independent branches = %v", seen)
	}
	close(releases["right"])
	close(releases["left"])
	select {
	case outcome := <-outcomes:
		if outcome.err != nil || outcome.value != http.StatusNoContent {
			t.Fatalf("independent outcome = %+v, want status 204", outcome)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("independent reply did not finish")
	}
	fmt.Fprintln(os.Stdout, "conftamer-workload: independent-release:right,left")
}

func testConftamerFanIn(t *testing.T) {
	started := make(chan string, 2)
	releases := map[string]chan struct{}{"left": make(chan struct{}), "right": make(chan struct{})}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		branch := strings.TrimPrefix(request.URL.Path, "/backend/fanin/")
		if branch == "sink" {
			w.WriteHeader(http.StatusCreated)
			return
		}
		started <- branch
		<-releases[branch]
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	frontend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, incoming *http.Request) {
		responses, err := conftamerFetchAll(incoming, []string{
			backend.URL + "/backend/fanin/left",
			backend.URL + "/backend/fanin/right",
		})
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer func() {
			for _, response := range responses {
				response.Body.Close()
			}
		}()
		sources := []http.ConftamerSource{incoming, responses[0], responses[1]}
		sinkRequest := conftamerMust(t, conftamerResultOf(http.NewRequestWithContext(
			incoming.Context(), http.MethodPost, backend.URL+"/backend/fanin/sink", nil,
		)))
		sinkRequest = http.ConftamerWithSources(sinkRequest, sources...)
		sinkResponse := conftamerMust(t, conftamerResultOf(http.DefaultClient.Do(sinkRequest)))
		defer sinkResponse.Body.Close()
		http.ConftamerSetReplySources(incoming, sinkResponse)
		w.WriteHeader(sinkResponse.StatusCode)
	}))
	defer frontend.Close()

	outcomes := make(chan conftamerResult[int], 1)
	go func() { outcomes <- conftamerGetStatus(frontend.URL + "/precision/fanin") }()
	for range 2 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("fan-in branch did not start")
		}
	}
	close(releases["left"])
	close(releases["right"])
	select {
	case outcome := <-outcomes:
		if outcome.err != nil || outcome.value != http.StatusCreated {
			t.Fatalf("fan-in outcome = %+v, want status 201", outcome)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fan-in reply did not finish")
	}
	fmt.Fprintln(os.Stdout, "conftamer-workload: fanin-release:left,right:201")
}

func testConftamerRepeatedRequest(t *testing.T) {
	var calls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		status := http.StatusOK
		if calls.Add(1) == 2 {
			status = http.StatusAccepted
		}
		w.WriteHeader(status)
	}))
	defer backend.Close()
	frontend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, incoming *http.Request) {
		sources := []http.ConftamerSource{incoming}
		var responses []*http.Response
		for range 2 {
			request := conftamerMust(t, conftamerResultOf(http.NewRequestWithContext(
				incoming.Context(), http.MethodGet, backend.URL+"/backend/repeated", nil,
			)))
			request = http.ConftamerWithSources(request, sources...)
			response := conftamerMust(t, conftamerResultOf(http.DefaultClient.Do(request)))
			defer response.Body.Close()
			responses = append(responses, response)
			sources = append(sources, response)
		}
		http.ConftamerSetReplySources(incoming, responses[0], responses[1])
		w.WriteHeader(responses[1].StatusCode)
	}))
	defer frontend.Close()

	conftamerRequest(t, http.DefaultClient, frontend.URL+"/precision/repeated",
		context.Background(), false, http.StatusAccepted, 1)
	fmt.Fprintln(os.Stdout, "conftamer-workload: repeated:200,202")
}

func conftamerFetchAll(incoming *http.Request, targets []string) ([]*http.Response, error) {
	results := make(chan conftamerResult[*http.Response], len(targets))
	for _, target := range targets {
		go func() {
			request, err := http.NewRequestWithContext(incoming.Context(), http.MethodGet, target, nil)
			if err == nil {
				request = http.ConftamerWithSources(request, incoming)
				var response *http.Response
				response, err = http.DefaultClient.Do(request)
				results <- conftamerResultOf(response, err)
				return
			}
			results <- conftamerResult[*http.Response]{err: err}
		}()
	}
	responses := make([]*http.Response, 0, len(targets))
	for range targets {
		result := <-results
		if result.err != nil {
			for _, response := range responses {
				response.Body.Close()
			}
			return nil, result.err
		}
		responses = append(responses, result.value)
	}
	return responses, nil
}

func conftamerGetStatus(target string) conftamerResult[int] {
	response, err := http.Get(target)
	status := 0
	if response != nil {
		status = response.StatusCode
		err = errors.Join(err, response.Body.Close())
	}
	return conftamerResultOf(status, err)
}

func conftamerForward(t *testing.T, w http.ResponseWriter, incoming *http.Request, target string) {
	t.Helper()
	outbound := conftamerMust(t, conftamerResultOf(http.NewRequestWithContext(
		incoming.Context(), http.MethodGet, target, nil,
	)))
	outbound = http.ConftamerWithSources(outbound, incoming)
	downstream := conftamerMust(t, conftamerResultOf(http.DefaultClient.Do(outbound)))
	defer downstream.Body.Close()
	http.ConftamerSetReplySources(incoming, downstream)
	w.WriteHeader(downstream.StatusCode)
}

func conftamerSourceWorkload(t *testing.T, w http.ResponseWriter, incoming *http.Request, backendURL string) {
	sourceRequest := conftamerMust(t, conftamerResultOf(http.NewRequest(http.MethodGet, backendURL+"/backend/source", nil)))
	sourceResponse := conftamerMust(t, conftamerResultOf(http.DefaultClient.Do(sourceRequest)))
	sourceResponse.Body.Close()

	outbound := conftamerMust(t, conftamerResultOf(http.NewRequestWithContext(incoming.Context(), http.MethodGet, backendURL+"/backend/sink", nil)))
	originalContext := outbound.Context()
	declaration := []http.ConftamerSource{sourceResponse, incoming, sourceResponse}
	annotated := http.ConftamerWithSources(outbound, declaration...)
	declaration[0] = incoming
	if annotated == outbound || annotated.Context() != originalContext || outbound.Context() != originalContext {
		t.Fatal("request annotation changed original request or context")
	}
	annotated = annotated.WithContext(annotated.Context())
	sinkResponse := conftamerMust(t, conftamerResultOf(http.DefaultClient.Do(annotated)))
	sinkResponse.Body.Close()

	declaration = []http.ConftamerSource{sinkResponse, sourceResponse, incoming, sinkResponse}
	http.ConftamerSetReplySources(incoming, declaration...)
	declaration[0] = incoming
	w.WriteHeader(http.StatusNoContent)
}

func conftamerTimeoutHandler(t *testing.T, started chan<- struct{}, release <-chan struct{}, finished chan<- struct{}, declareLate bool) http.Handler {
	late := http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		defer close(finished)
		started <- struct{}{}
		<-release
		if declareLate {
			http.ConftamerSetReplySources(request, request)
		}
		if _, err := io.WriteString(w, "late"); !errors.Is(err, http.ErrHandlerTimeout) {
			t.Errorf("late timeout write error = %v, want ErrHandlerTimeout", err)
		}
	})
	return http.TimeoutHandler(late, 25*time.Millisecond, "timeout")
}

func testConftamerParallelWorkload(t *testing.T, client *http.Client, serverURL string) {
	results := make(chan conftamerParallelResult, conftamerParallelRequests)
	var workers sync.WaitGroup
	for index := range conftamerParallelRequests {
		workers.Add(1)
		go func() {
			defer workers.Done()
			request, err := http.NewRequestWithContext(
				context.Background(),
				http.MethodGet,
				fmt.Sprintf("%s/parallel/%d", serverURL, index),
				nil,
			)
			if err != nil {
				results <- conftamerParallelResult{index: index, err: err}
				return
			}
			originalContext := request.Context()
			response, err := client.Do(request)
			if err != nil {
				results <- conftamerParallelResult{index: index, err: err}
				return
			}
			_, readErr := io.Copy(io.Discard, response.Body)
			closeErr := response.Body.Close()
			if readErr != nil {
				err = readErr
			} else if closeErr != nil {
				err = closeErr
			} else if request.Context() != originalContext || response.Request != request {
				err = errors.New("parallel request or context identity changed")
			}
			results <- conftamerParallelResult{
				index: index, statusCode: response.StatusCode, protocol: response.ProtoMajor, err: err,
			}
		}()
	}
	workers.Wait()
	close(results)

	outcomes := make([]string, 0, conftamerParallelRequests)
	for result := range results {
		if result.err != nil {
			t.Fatalf("parallel request %d: %v", result.index, result.err)
		}
		if result.statusCode != http.StatusNoContent || result.protocol != 1 {
			t.Fatalf("parallel request %d status/protocol = %d/%d", result.index, result.statusCode, result.protocol)
		}
		outcomes = append(outcomes, fmt.Sprintf("%d:%d:%d", result.index, result.statusCode, result.protocol))
	}
	sort.Strings(outcomes)
	fmt.Fprintf(os.Stdout, "conftamer-workload: %s\n", strings.Join(outcomes, ","))
}

func testConftamerTimeoutWorkload(t *testing.T, client *http.Client, serverURL string, started <-chan struct{}, release chan<- struct{}, finished <-chan struct{}) {
	type result struct {
		response *http.Response
		err      error
	}
	request := conftamerMust(t, conftamerResultOf(http.NewRequestWithContext(
		context.Background(), http.MethodGet, serverURL+"/timeout/7", nil,
	)))
	results := make(chan result, 1)
	go func() {
		response, err := client.Do(request)
		results <- result{response: response, err: err}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout handler did not start")
	}
	var outcome result
	select {
	case outcome = <-results:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout response was not received")
	}
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	body, readErr := io.ReadAll(outcome.response.Body)
	closeErr := outcome.response.Body.Close()
	if readErr != nil || closeErr != nil || outcome.response.StatusCode != http.StatusServiceUnavailable || string(body) != "timeout" {
		t.Fatalf("timeout response body/status/errors = %q/%d/%v/%v", body, outcome.response.StatusCode, readErr, closeErr)
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("late timeout handler did not finish")
	}
}

func testConftamerBodyAndTrailers(t *testing.T, client *http.Client, serverURL string) {
	body := &conftamerCountingBody{reader: strings.NewReader("request body")}
	request := conftamerMust(t, conftamerResultOf(http.NewRequestWithContext(
		context.Background(), http.MethodPost, serverURL+"/behavior/data", body,
	)))
	response := conftamerMust(t, conftamerResultOf(client.Do(request)))
	data, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || string(data) != "request body" || response.Trailer.Get("X-Conftamer-Trailer") != "complete" {
		t.Fatalf("response data/trailer/errors = %q/%q/%v/%v", data, response.Trailer.Get("X-Conftamer-Trailer"), readErr, closeErr)
	}
	if body.reads == 0 || body.closes != 1 || response.Request != request {
		t.Fatalf("body reads/closes or request identity = %d/%d/%t", body.reads, body.closes, response.Request == request)
	}
}

func testConftamerCancellation(t *testing.T, serverURL string, started <-chan struct{}) {
	run := func(client *http.Client, request *http.Request, cancel func()) error {
		result := make(chan error, 1)
		go func() {
			response, err := client.Do(request)
			if response != nil {
				response.Body.Close()
			}
			result <- err
		}()
		<-started
		cancel()
		return <-result
	}
	newRequest := func(ctx context.Context) *http.Request {
		return conftamerMust(t, conftamerResultOf(http.NewRequestWithContext(ctx, http.MethodGet, serverURL+"/behavior/cancel", nil)))
	}

	ctx, cancel := context.WithCancel(context.Background())
	if err := run(http.DefaultClient, newRequest(ctx), cancel); !errors.Is(err, context.Canceled) {
		t.Fatalf("context cancellation error = %v", err)
	}
	legacy := make(chan struct{})
	legacyRequest := newRequest(context.Background())
	legacyRequest.Cancel = legacy
	if err := run(http.DefaultClient, legacyRequest, func() { close(legacy) }); err == nil || !strings.Contains(err.Error(), "request canceled") {
		t.Fatalf("Request.Cancel error = %v", err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	transportRequest := newRequest(context.Background())
	if err := run(&http.Client{Transport: transport}, transportRequest, func() { transport.CancelRequest(transportRequest) }); err == nil || !strings.Contains(err.Error(), "request canceled") {
		t.Fatalf("CancelRequest error = %v", err)
	}
	deadline, stop := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer stop()
	response, err := http.DefaultClient.Do(newRequest(deadline))
	if response != nil {
		response.Body.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline error = %v", err)
	}
}

func testConftamerRedirect(t *testing.T, serverURL string) {
	source := conftamerMust(t, conftamerResultOf(http.Get(serverURL+"/redirect/source")))
	source.Body.Close()
	var redirected *http.Request
	client := &http.Client{CheckRedirect: func(next *http.Request, via []*http.Request) error {
		if len(via) != 1 || next.Response == nil || next.Response.Request != via[0] {
			return errors.New("unexpected redirect chain")
		}
		annotated := http.ConftamerWithSources(next, next.Response)
		*next = *annotated
		redirected = next
		return nil
	}}
	request := conftamerMust(t, conftamerResultOf(http.NewRequestWithContext(
		context.Background(), http.MethodGet, serverURL+"/redirect/start", nil,
	)))
	request = http.ConftamerWithSources(request, source)
	originalContext := request.Context()
	response := conftamerMust(t, conftamerResultOf(client.Do(request)))
	response.Body.Close()
	if redirected == nil || redirected.Context() != originalContext || request.Context() != originalContext || response.Request != redirected {
		t.Fatal("redirect changed request or context identity")
	}
}

func testConftamerRetry(t *testing.T, serverURL string) {
	source := conftamerMust(t, conftamerResultOf(http.Get(serverURL+"/retry/source")))
	source.Body.Close()
	writeCount := new(atomic.Int32)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		connection, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &conftamerFailWriteConn{Conn: connection, writeCount: writeCount}, nil
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	retried := make(chan struct{}, 1)
	http.SetRoundTripRetried(func() { retried <- struct{}{} })
	defer http.SetRoundTripRetried(nil)
	for _, path := range []string{"/retry/1", "/retry/2", "/retry/3"} {
		request := conftamerMust(t, conftamerResultOf(http.NewRequest(http.MethodGet, serverURL+path, nil)))
		request = http.ConftamerWithSources(request, source)
		response := conftamerMust(t, conftamerResultOf(client.Do(request)))
		response.Body.Close()
		if response.StatusCode != http.StatusNoContent || response.Request != request {
			t.Fatalf("retry response status/request = %d/%t", response.StatusCode, response.Request == request)
		}
	}
	select {
	case <-retried:
	default:
		t.Fatal("transport did not report a retry")
	}
}

func conftamerRequest(t *testing.T, client *http.Client, target string, ctx context.Context, direct bool, wantStatus, wantProtocol int) {
	t.Helper()
	request := conftamerMust(t, conftamerResultOf(http.NewRequestWithContext(ctx, http.MethodGet, target, nil)))
	var err error
	originalContext := request.Context()
	var response *http.Response
	if direct {
		response, err = http.DefaultTransport.RoundTrip(request)
	} else {
		response, err = client.Do(request)
	}
	response = conftamerMust(t, conftamerResultOf(response, err))
	defer response.Body.Close()
	var readErr error
	if response.StatusCode != http.StatusSwitchingProtocols {
		_, readErr = io.Copy(io.Discard, response.Body)
	}
	if readErr != nil || response.StatusCode != wantStatus || response.ProtoMajor != wantProtocol || request.Context() != originalContext || response.Request != request {
		t.Fatalf("response/read/context = %v/%v/%d/%d/%t/%t", err, readErr, response.StatusCode, response.ProtoMajor, request.Context() == originalContext, response.Request == request)
	}
}

func captureConftamerMode(t *testing.T, mode string) []conftamerCapturedRecord {
	t.Helper()
	records, _ := captureConftamerModeResult(t, mode)
	return records
}

func captureConftamerModeResult(t *testing.T, mode string) ([]conftamerCapturedRecord, string) {
	t.Helper()
	records, _, stderr := captureConftamerModeOutput(t, mode)
	return records, stderr
}

func captureConftamerModeOutput(t *testing.T, mode string) ([]conftamerCapturedRecord, string, string) {
	t.Helper()
	directory := t.TempDir()
	child := newConftamerChildProcess(context.Background(), mode, map[string]string{
		"CONFTAMER_EVENTS_DIR": directory,
		"CONFTAMER_CAPTURE_ID": "unit-capture",
	})
	if err := child.command.Run(); err != nil {
		t.Fatalf("capture child failed: %v\nstdout: %s\nstderr: %s", err, child.stdout.String(), child.stderr.String())
	}
	waitConftamerChild(t, child, true)
	return readConftamerRecords(t, directory), child.stdout.String(), child.stderr.String()
}

func runConftamerChild(t *testing.T, mode string, settings map[string]string, wantEnabled bool) string {
	t.Helper()
	child := newConftamerChildProcess(context.Background(), mode, settings)
	if err := child.command.Run(); err != nil {
		t.Fatalf("capture child failed: %v\nstdout: %s\nstderr: %s", err, child.stdout.String(), child.stderr.String())
	}
	waitConftamerChild(t, child, wantEnabled)
	return child.stdout.String()
}

func waitConftamerChild(t *testing.T, child *conftamerChildProcess, wantEnabled bool) {
	t.Helper()
	if child.command.ProcessState == nil {
		if err := child.command.Wait(); err != nil {
			t.Fatalf("capture child failed: %v\nstdout: %s\nstderr: %s", err, child.stdout.String(), child.stderr.String())
		}
	} else if !child.command.ProcessState.Success() {
		t.Fatalf("capture child failed:\nstdout: %s\nstderr: %s", child.stdout.String(), child.stderr.String())
	}
	if got := strings.Contains(child.stderr.String(), "conftamer: enabled"); got != wantEnabled {
		t.Fatalf("enabled diagnostic = %t, want %t; stderr: %q", got, wantEnabled, child.stderr.String())
	}
}

func assertConftamerParallelRecords(t *testing.T, records []conftamerCapturedRecord, captureID, processID string) {
	t.Helper()
	if len(records) != conftamerParallelRequests*4 {
		t.Fatalf("process %s records = %d, want %d", processID, len(records), conftamerParallelRequests*4)
	}
	exchanges := make(map[uint64][]string)
	for index, record := range records {
		if record.SchemaVersion != 4 || record.CaptureID != captureID || record.ProcessID != processID || record.Seq != uint64(index+1) {
			t.Fatalf("process %s record %d envelope = %+v", processID, index+1, record)
		}
		exchanges[record.ExchangeID] = append(exchanges[record.ExchangeID], record.Kind)
		if record.Kind == "send_response" {
			if len(record.Sources) != 1 {
				t.Fatalf("process %s exchange %d reply sources = %v", processID, record.ExchangeID, record.Sources)
			}
		} else if len(record.Sources) != 0 {
			t.Fatalf("process %s exchange %d %s sources = %v", processID, record.ExchangeID, record.Kind, record.Sources)
		}
	}
	if len(exchanges) != conftamerParallelRequests*2 {
		t.Fatalf("process %s exchange count = %d, want %d", processID, len(exchanges), conftamerParallelRequests*2)
	}
	for id, kinds := range exchanges {
		sort.Strings(kinds)
		pair := strings.Join(kinds, ",")
		if pair != "receive_request,send_response" && pair != "receive_response,send_request" {
			t.Fatalf("process %s exchange %d kinds = %v", processID, id, kinds)
		}
	}
}

func readConftamerRecords(t *testing.T, directory string) []conftamerCapturedRecord {
	t.Helper()
	processes := readConftamerProcessRecords(t, directory)
	if len(processes) != 1 {
		t.Fatalf("process files = %d, want 1", len(processes))
	}
	for _, records := range processes {
		return records
	}
	return nil
}

func readConftamerProcessRecords(t *testing.T, directory string) map[string][]conftamerCapturedRecord {
	t.Helper()
	files := conftamerMust(t, conftamerResultOf(filepath.Glob(filepath.Join(directory, "*.jsonl"))))
	processes := make(map[string][]conftamerCapturedRecord, len(files))
	for _, path := range files {
		content := conftamerMust(t, conftamerResultOf(os.ReadFile(path)))
		processID := strings.TrimSuffix(filepath.Base(path), ".jsonl")
		var records []conftamerCapturedRecord
		for physicalLine, line := range bytes.Split(content, []byte{'\n'}) {
			if len(line) == 0 {
				continue
			}
			var record conftamerCapturedRecord
			if err := json.Unmarshal(line, &record); err != nil {
				t.Fatalf("%s:%d: %v", path, physicalLine+1, err)
			}
			records = append(records, record)
		}
		processes[processID] = records
	}
	return processes
}

func TestConftamerConfiguration(t *testing.T) {
	if stderr := runConftamerConfigurationChild(t, nil); strings.Contains(stderr, "conftamer:") {
		t.Fatalf("disabled diagnostics = %q", stderr)
	}
	for _, test := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "missing capture ID", env: map[string]string{"CONFTAMER_EVENTS_DIR": t.TempDir()}, want: "must both be set"},
		{name: "missing events directory", env: map[string]string{"CONFTAMER_CAPTURE_ID": "unit"}, want: "must both be set"},
		{name: "relative directory", env: map[string]string{"CONFTAMER_EVENTS_DIR": "relative", "CONFTAMER_CAPTURE_ID": "unit"}, want: "must be absolute"},
		{name: "legacy setting", env: map[string]string{"CONFTAMER_EVENTS": "/tmp/legacy"}, want: "CONFTAMER_EVENTS is unsupported"},
		{name: "legacy wins over v4", env: map[string]string{"CONFTAMER_EVENTS": "/tmp/legacy", "CONFTAMER_EVENTS_DIR": t.TempDir(), "CONFTAMER_CAPTURE_ID": "unit"}, want: "CONFTAMER_EVENTS is unsupported"},
	} {
		t.Run(test.name, func(t *testing.T) {
			stderr := runConftamerConfigurationChild(t, test.env)
			if !strings.Contains(stderr, "conftamer: configuration error:") || !strings.Contains(stderr, test.want) {
				t.Fatalf("diagnostics = %q, want %q", stderr, test.want)
			}
			if directory := test.env["CONFTAMER_EVENTS_DIR"]; filepath.IsAbs(directory) {
				entries, err := os.ReadDir(directory)
				if err != nil || len(entries) != 0 {
					t.Fatalf("directory entries/error = %v/%v", entries, err)
				}
			}
		})
	}

	directory := t.TempDir()
	stderr := runConftamerConfigurationChild(t, map[string]string{"CONFTAMER_EVENTS_DIR": directory, "CONFTAMER_CAPTURE_ID": "unit-capture"})
	files, _ := filepath.Glob(filepath.Join(directory, "*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("process files = %v", files)
	}
	processID := strings.TrimSuffix(filepath.Base(files[0]), ".jsonl")
	info, err := os.Stat(files[0])
	if err != nil || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(processID) || info.Mode().Perm() != 0o600 {
		t.Fatalf("process file identity/mode/error = %q/%v/%v", processID, info.Mode().Perm(), err)
	}
	for _, want := range []string{"conftamer: enabled", "capture_id=\"unit-capture\"", "process_id=\"" + processID + "\"", "path=\"" + files[0] + "\""} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("enabled diagnostics = %q, missing %q", stderr, want)
		}
	}
}

func runConftamerConfigurationChild(t *testing.T, settings map[string]string) string {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^$", "-test.count=1")
	command.Env = conftamerEnvironment(settings)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if output, err := command.Output(); err != nil {
		t.Fatalf("configuration child failed: %v\nstdout: %s\nstderr: %s", err, output, stderr.String())
	}
	return stderr.String()
}

func conftamerEnvironment(overrides map[string]string) []string {
	environment := make([]string, 0, len(os.Environ())+len(overrides))
	for _, setting := range os.Environ() {
		name, _, _ := strings.Cut(setting, "=")
		if !conftamerConfigurationNames[name] {
			environment = append(environment, setting)
		}
	}
	for name, value := range overrides {
		environment = append(environment, name+"="+value)
	}
	return environment
}
