package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	conftamerCaptureChild       = "CONFTAMER_CAPTURE_TEST_CHILD"
	conftamerConfigurationChild = "CONFTAMER_CONFIGURATION_TEST_CHILD"
	conftamerParallelRequests   = 12
)

var conftamerConfigurationNames = map[string]bool{
	"CONFTAMER_EVENTS":          true,
	"CONFTAMER_EVENTS_DIR":      true,
	"CONFTAMER_CAPTURE_ID":      true,
	"GODEBUG":                   true,
	conftamerCaptureChild:       true,
	conftamerConfigurationChild: true,
}

type conftamerCapturedRoute struct {
	Dialect     string  `json:"dialect"`
	Pattern     string  `json:"pattern"`
	MatchedPath string  `json:"matched_path"`
	FullPattern *string `json:"full_pattern"`
}

type conftamerCapturedRecord struct {
	SchemaVersion int     `json:"schema_version"`
	CaptureID     string  `json:"capture_id"`
	ProcessID     string  `json:"process_id"`
	Seq           uint64  `json:"seq"`
	ExchangeID    uint64  `json:"exchange_id"`
	Kind          string  `json:"kind"`
	ContextID     *uint64 `json:"context_id"`
	Request       struct {
		Method string  `json:"method"`
		Host   *string `json:"host"`
		Path   string  `json:"path"`
	} `json:"request"`
	APIID      *string                 `json:"api_id"`
	Route      *conftamerCapturedRoute `json:"route"`
	StatusCode int                     `json:"status_code"`
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
	mux := http.NewServeMux()
	mux.HandleFunc("GET /items/{id}", func(w http.ResponseWriter, r *http.Request) {
		http.ConftamerSetServerAPI(r, "example.org/items")
		w.WriteHeader(http.StatusOK)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	ctx := http.ConftamerContext(context.Background())
	req, err := http.NewRequestWithContext(ctx, "GET", server.URL+"/items/7", nil)
	if err != nil {
		t.Fatal(err)
	}
	req = http.ConftamerWithClientAPI(req, "example.org/items")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
}

func TestConftamerParallelAndMultiprocessCaptures(t *testing.T) {
	directory := t.TempDir()
	settings := map[string]string{
		"CONFTAMER_EVENTS_DIR": directory,
		"CONFTAMER_CAPTURE_ID": "parallel-capture",
	}
	enabled := runConftamerChild(t, "parallel", settings, true)
	disabled := runConftamerChild(t, "parallel", nil, false)
	if conftamerWorkloadMarker(t, enabled) != conftamerWorkloadMarker(t, disabled) {
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
		newConftamerChildProcess("parallel", settings),
		newConftamerChildProcess("parallel", settings),
	}
	for _, child := range children {
		if err := child.command.Start(); err != nil {
			t.Fatal(err)
		}
	}
	for _, child := range children {
		waitConftamerChild(t, child, true)
		if conftamerWorkloadMarker(t, child.stdout.String()) != conftamerWorkloadMarker(t, disabled) {
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
	if len(records) != 6 {
		t.Fatalf("timeout records = %d, want 6: %+v", len(records), records)
	}
	server := conftamerOnlyRecord(t, records, "receive_request", "/timeout/7")
	if server.ContextID == nil {
		t.Fatal("timeout server request has unknown context")
	}
	response := conftamerRecordForExchange(t, records, "send_response", server.ExchangeID)
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("timeout response status = %d, want %d", response.StatusCode, http.StatusServiceUnavailable)
	}
	metadata := conftamerMetadataForExchange(records, server.ExchangeID)
	if len(metadata) != 2 {
		t.Fatalf("late timeout metadata = %d records, want route and API", len(metadata))
	}
	for _, record := range metadata {
		if record.Seq <= response.Seq {
			t.Fatalf("metadata seq = %d, want after final response seq %d", record.Seq, response.Seq)
		}
	}
	if route := metadata[0].Route; route == nil || route.FullPattern == nil || *route.FullPattern != "/timeout/:id" {
		t.Fatalf("late timeout route = %+v", route)
	}
	if got := conftamerAPIValue(metadata[1]); got != "example.org/timeout" {
		t.Fatalf("late timeout API = %q", got)
	}
}

func TestConftamerExchangeOwnership(t *testing.T) {
	for _, test := range []struct {
		name, mode string
		http2      bool
	}{
		{name: "HTTP/1", mode: "http1"},
		{name: "HTTP/2", mode: "http2", http2: true},
		{name: "direct RoundTrip", mode: "direct"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.http2 {
				http.CondSkipHTTP2(t)
			}
			assertConftamerExchange(t, captureConftamerMode(t, test.mode))
		})
	}
}

func TestConftamerContextLifetimes(t *testing.T) {
	records := captureConftamerMode(t, "contexts")
	if len(records) != 8 {
		t.Fatalf("record count = %d, want 8", len(records))
	}
	requests := conftamerRecords(records, "", "request")
	wantKinds := []string{"send_request", "receive_request", "send_request", "receive_request"}
	wantContexts := []uint64{0, 1, 2, 3}
	if len(requests) != len(wantKinds) {
		t.Fatalf("request records = %d, want %d", len(requests), len(wantKinds))
	}
	for index, record := range requests {
		if record.Kind != wantKinds[index] || conftamerContextValue(record) != wantContexts[index] {
			t.Fatalf("request %d = (%s, %d), want (%s, %d)", index, record.Kind, conftamerContextValue(record), wantKinds[index], wantContexts[index])
		}
	}
}

func TestConftamerContextDisabledIsIdentity(t *testing.T) {
	if os.Getenv("CONFTAMER_EVENTS_DIR") != "" || os.Getenv("CONFTAMER_CAPTURE_ID") != "" {
		t.Skip("requires capture-disabled parent process")
	}
	ctx := context.WithValue(context.Background(), struct{ name string }{"existing"}, true)
	if http.ConftamerContext(ctx) != ctx || http.ConftamerContext(nil) != nil {
		t.Fatal("disabled ConftamerContext changed its argument")
	}
	request := &http.Request{}
	if http.ConftamerWithClientAPI(request, "example.org/api") != request || http.ConftamerWithClientAPI(nil, "example.org/api") != nil {
		t.Fatal("disabled ConftamerWithClientAPI changed its argument")
	}
}

func TestConftamerInvalidRequestEmitsNothing(t *testing.T) {
	if records := captureConftamerMode(t, "invalid"); len(records) != 0 {
		t.Fatalf("invalid request records = %+v, want none", records)
	}
}

func TestConftamerAttemptLifetimes(t *testing.T) {
	t.Run("server request copied outbound", func(t *testing.T) {
		records := captureConftamerMode(t, "nested")
		requests := conftamerRecords(records, "", "request")
		if len(records) != 8 || len(requests) != 4 {
			t.Fatalf("records/requests = %d/%d, want 8/4", len(records), len(requests))
		}
		seen := make(map[uint64]bool)
		for _, record := range requests {
			seen[record.ExchangeID] = true
		}
		outer := conftamerOnlyRecord(t, records, "receive_request", "/nested")
		inner := conftamerOnlyRecord(t, records, "send_request", "/backend")
		if len(seen) != 4 || outer.ContextID == nil || conftamerContextValue(outer) != conftamerContextValue(inner) {
			t.Fatalf("request exchanges/contexts = %v/%v/%v", seen, outer.ContextID, inner.ContextID)
		}
	})

	t.Run("redirect", func(t *testing.T) {
		records := captureConftamerMode(t, "redirect")
		attempts := conftamerRecords(records, "send_request", "/redirect/")
		assertConftamerAttempts(t, records, attempts, 2)
		if conftamerAPIValue(attempts[0]) != "example.org/redirect" || attempts[1].APIID != nil {
			t.Fatalf("redirect API bindings = %q/%v, want explicit then unknown", conftamerAPIValue(attempts[0]), attempts[1].APIID)
		}
	})

	t.Run("retry", func(t *testing.T) {
		records := captureConftamerMode(t, "retry")
		attempts := conftamerRecords(records, "send_request", "/retry/2")
		assertConftamerAttempts(t, records, attempts, 1)
		if got := len(conftamerRecords(records, "receive_request", "/retry/2")); got != 1 {
			t.Fatalf("retried server requests = %d, want 1", got)
		}
	})
}

func TestConftamerRoutingAndAPIBindings(t *testing.T) {
	records := captureConftamerMode(t, "metadata")
	client := conftamerOnlyRecord(t, records, "send_request", "/front/items/7")
	server := conftamerOnlyRecord(t, records, "receive_request", "/front/items/7")
	outbound := conftamerOnlyRecord(t, records, "send_request", "/backend")
	if conftamerAPIValue(client) != "example.org/b" || conftamerAPIValue(outbound) != "example.org/c" {
		t.Fatalf("client/outbound API bindings = %q/%q", conftamerAPIValue(client), conftamerAPIValue(outbound))
	}
	if server.APIID != nil || server.ContextID == nil || conftamerContextValue(server) != conftamerContextValue(outbound) {
		t.Fatalf("server API/context leaked or diverged: server=%v/%v outbound=%v/%v", server.APIID, server.ContextID, outbound.APIID, outbound.ContextID)
	}

	metadata := conftamerMetadataForExchange(records, server.ExchangeID)
	if len(metadata) != 3 {
		t.Fatalf("server metadata count = %d, want route plus two API bindings", len(metadata))
	}
	if route := metadata[0].Route; route == nil || route.Dialect != "go_serve_mux" || route.Pattern != "GET module-b.test/items/{id}" || route.MatchedPath != "/items/7" || route.FullPattern == nil || *route.FullPattern != "GET module-b.test/front/items/{id}" {
		t.Fatalf("ServeMux route metadata = %+v", route)
	}
	for index, record := range metadata[1:] {
		if record.Route != nil || conftamerAPIValue(record) != "example.org/b" {
			t.Fatalf("server API metadata %d = route %+v API %q", index+1, record.Route, conftamerAPIValue(record))
		}
	}
	if record := conftamerOnlyRecord(t, records, "send_request", "/front/unbound"); record.APIID != nil {
		t.Fatalf("discarded client binding mutated its input: API = %v", record.APIID)
	}
	if record := conftamerOnlyRecord(t, records, "send_request", "/front/changed"); record.APIID != nil {
		t.Fatalf("changed client target retained stale API binding: API = %v", record.APIID)
	}
}

func TestConftamerServeMux121Routing(t *testing.T) {
	records := captureConftamerModeWithSettings(t, "route121", map[string]string{"GODEBUG": "httpmuxgo121=1"})
	request := conftamerOnlyRecord(t, records, "receive_request", "/legacy/item")
	metadata := conftamerMetadataForExchange(records, request.ExchangeID)
	if len(metadata) != 1 {
		t.Fatalf("Go 1.21 metadata count = %d, want 1", len(metadata))
	}
	route := metadata[0].Route
	if route == nil || route.Dialect != "go_serve_mux_121" || route.Pattern != "/legacy/" || route.MatchedPath != "/legacy/item" || route.FullPattern == nil || *route.FullPattern != "/legacy/" {
		t.Fatalf("Go 1.21 route metadata = %+v", route)
	}
}

func TestConftamerResponseLifecycles(t *testing.T) {
	for _, protocol := range []string{"http1", "http2"} {
		t.Run(protocol, func(t *testing.T) {
			if protocol == "http2" {
				http.CondSkipHTTP2(t)
			}
			want := map[string]int{
				"/empty": 200, "/explicit": 200, "/informational": 200,
				"/implicit": 200, "/repeated": 202,
			}
			if protocol == "http1" {
				want["/switch"] = 101
			}
			assertConftamerLifecycles(t, captureConftamerMode(t, "lifecycle-"+protocol), want)
		})
	}
}

func TestConftamerHTTPBehaviorPreserved(t *testing.T) {
	for _, mode := range []string{"behavior-data", "behavior-cancel"} {
		for _, enabled := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "-disabled", true: "-enabled"}[enabled], func(t *testing.T) {
				if !enabled {
					runConftamerChild(t, mode, nil, false)
					return
				}
				if records := captureConftamerMode(t, mode); len(records) == 0 {
					t.Fatal("enabled preservation workload emitted no records")
				}
			})
		}
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
	if client.ContextID == nil || server.ContextID == nil || conftamerContextValue(client) == conftamerContextValue(server) {
		t.Fatalf("request context IDs = (%v, %v), want distinct known roots", client.ContextID, server.ContextID)
	}
}

func assertConftamerAttempts(t *testing.T, records, attempts []conftamerCapturedRecord, wantResponses int) {
	t.Helper()
	if len(attempts) != 2 || attempts[0].ExchangeID == attempts[1].ExchangeID || attempts[0].ContextID == nil || conftamerContextValue(attempts[0]) != conftamerContextValue(attempts[1]) {
		t.Fatalf("attempts = %+v, want two exchanges sharing one known root", attempts)
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

func conftamerOnlyRecord(t *testing.T, records []conftamerCapturedRecord, kind, path string) conftamerCapturedRecord {
	t.Helper()
	matches := conftamerRecords(records, kind, path)
	if len(matches) != 1 {
		t.Fatalf("%s %s records = %d, want 1", kind, path, len(matches))
	}
	return matches[0]
}

func conftamerRecordForExchange(t *testing.T, records []conftamerCapturedRecord, kind string, exchangeID uint64) conftamerCapturedRecord {
	t.Helper()
	var matches []conftamerCapturedRecord
	for _, record := range records {
		if record.Kind == kind && record.ExchangeID == exchangeID {
			matches = append(matches, record)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("%s exchange %d records = %d, want 1", kind, exchangeID, len(matches))
	}
	return matches[0]
}

func conftamerContextValue(record conftamerCapturedRecord) uint64 {
	if record.ContextID == nil {
		return 0
	}
	return *record.ContextID
}

func conftamerAPIValue(record conftamerCapturedRecord) string {
	if record.APIID == nil {
		return ""
	}
	return *record.APIID
}

func conftamerMetadataForExchange(records []conftamerCapturedRecord, exchangeID uint64) []conftamerCapturedRecord {
	var metadata []conftamerCapturedRecord
	for _, record := range records {
		if record.Kind == "request_metadata" && record.ExchangeID == exchangeID {
			metadata = append(metadata, record)
		}
	}
	return metadata
}

func TestConftamerCaptureChild(t *testing.T) {
	mode := os.Getenv(conftamerCaptureChild)
	if mode == "" {
		t.Skip("helper process")
	}
	if mode == "invalid" {
		testConftamerInvalidRequest(t)
		return
	}

	started := make(chan struct{}, 1)
	timeoutRelease := make(chan struct{})
	timeoutFinished := make(chan struct{})
	var backendURL string
	if mode == "nested" || mode == "metadata" {
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
		defer backend.Close()
		backendURL = backend.URL
	}
	var handler http.Handler
	switch mode {
	case "metadata":
		handler = conftamerMetadataHandler(t, backendURL)
	case "timeout":
		handler = conftamerTimeoutHandler(t, started, timeoutRelease, timeoutFinished)
	case "route121":
		mux := http.NewServeMux()
		mux.HandleFunc("/legacy/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
		handler = mux
	default:
		handler = conftamerTestHandler(t, backendURL, started)
	}
	server := httptest.NewServer(handler)
	if mode == "http2" || mode == "lifecycle-http2" {
		server.Close()
		server = httptest.NewUnstartedServer(handler)
		server.EnableHTTP2 = true
		server.StartTLS()
	}
	defer server.Close()
	client := http.DefaultClient
	if server.TLS != nil {
		client = server.Client()
	}

	switch mode {
	case "behavior-data":
		testConftamerBodyAndTrailers(t, client, server.URL)
	case "behavior-cancel":
		testConftamerCancellation(t, server.URL, started)
	case "redirect":
		testConftamerRedirect(t, server.URL)
	case "retry":
		testConftamerRetry(t, server.URL)
	case "metadata":
		testConftamerMetadataRequests(t, client, server.URL)
	case "parallel":
		testConftamerParallelWorkload(t, client, server.URL)
	case "timeout":
		testConftamerTimeoutWorkload(t, client, server.URL, started, timeoutRelease, timeoutFinished)
	case "route121":
		conftamerRequest(t, client, server.URL+"/legacy/item", http.ConftamerContext(context.Background()), false, 1)
	case "lifecycle-http1", "lifecycle-http2":
		paths := []string{"/empty", "/explicit", "/informational", "/implicit", "/repeated"}
		if mode == "lifecycle-http1" {
			paths = append(paths, "/switch")
		}
		for _, path := range paths {
			conftamerLifecycleRequest(t, client, server.URL+path, false)
		}
		conftamerLifecycleRequest(t, client, server.URL+"/panic", true)
	case "contexts":
		conftamerRequest(t, client, server.URL+"/unknown", context.Background(), false, 1)
		conftamerRequest(t, client, server.URL+"/rooted", http.ConftamerContext(context.Background()), false, 1)
	default:
		root := http.ConftamerContext(context.Background())
		if http.ConftamerContext(root) != root {
			t.Fatal("stamping an existing root changed its identity")
		}
		ctx := context.WithValue(root, struct{ name string }{"derived"}, true)
		path, direct, protocol := "/resource", mode == "direct", 1
		if mode == "nested" {
			path = "/nested"
		}
		if mode == "http2" {
			protocol = 2
		}
		conftamerRequest(t, client, server.URL+path, ctx, direct, protocol)
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
		case "/nested":
			outbound := new(http.Request)
			*outbound = *request
			outbound.URL, _ = url.Parse(backendURL + "/backend")
			outbound.Host, outbound.RequestURI, outbound.Body, outbound.ContentLength = "", "", nil, 0
			response, err := http.DefaultClient.Do(outbound)
			if err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			response.Body.Close()
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
}

func conftamerTimeoutHandler(t *testing.T, started chan<- struct{}, release <-chan struct{}, finished chan<- struct{}) http.Handler {
	late := http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		defer close(finished)
		started <- struct{}{}
		<-release
		http.ConftamerLogRouted(request, "httprouter", "/timeout/:id")
		http.ConftamerSetServerAPI(request, "example.org/timeout")
		if _, err := io.WriteString(w, "late"); !errors.Is(err, http.ErrHandlerTimeout) {
			t.Errorf("late timeout write error = %v, want ErrHandlerTimeout", err)
		}
	})
	return http.TimeoutHandler(late, 25*time.Millisecond, "timeout")
}

func conftamerMetadataHandler(t *testing.T, backendURL string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET module-b.test/items/{id}", func(w http.ResponseWriter, request *http.Request) {
		http.ConftamerSetServerAPI(request, "example.org/b")
		http.ConftamerSetServerAPI(request, "example.org/b")
		outbound, err := http.NewRequestWithContext(request.Context(), http.MethodGet, backendURL+"/backend", nil)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		response, err := http.DefaultClient.Do(http.ConftamerWithClientAPI(outbound, "example.org/c"))
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		response.Body.Close()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET module-b.test/unbound", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET module-b.test/changed", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	return http.StripPrefix("/front", mux)
}

func testConftamerParallelWorkload(t *testing.T, client *http.Client, serverURL string) {
	results := make(chan conftamerParallelResult, conftamerParallelRequests)
	var workers sync.WaitGroup
	for index := range conftamerParallelRequests {
		workers.Add(1)
		go func() {
			defer workers.Done()
			request, err := http.NewRequestWithContext(
				http.ConftamerContext(context.Background()),
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
	request, err := http.NewRequestWithContext(
		http.ConftamerContext(context.Background()), http.MethodGet, serverURL+"/timeout/7", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
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

func testConftamerMetadataRequests(t *testing.T, client *http.Client, serverURL string) {
	do := func(request *http.Request) {
		t.Helper()
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			t.Fatalf("metadata response status = %d", response.StatusCode)
		}
	}
	newRequest := func(path string) *http.Request {
		t.Helper()
		request, err := http.NewRequestWithContext(http.ConftamerContext(context.Background()), http.MethodGet, serverURL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Host = "module-b.test"
		return request
	}

	original := newRequest("/front/items/7")
	bound := http.ConftamerWithClientAPI(original, "example.org/b")
	if bound == original || bound.Context() != original.Context() || bound.Method != original.Method || bound.Host != original.Host || bound.URL != original.URL {
		t.Fatal("client API helper did not return an otherwise-identical shallow copy")
	}
	// An annotation on an untraced request must not invent an exchange.
	http.ConftamerSetServerAPI(&http.Request{}, "example.org/ignored")
	do(bound)

	unbound := newRequest("/front/unbound")
	_ = http.ConftamerWithClientAPI(unbound, "example.org/b")
	do(unbound)

	changed := http.ConftamerWithClientAPI(newRequest("/front/items/8"), "example.org/b")
	changedURL := *changed.URL
	changedURL.Path = "/front/changed"
	changed.URL = &changedURL
	do(changed)
}

func testConftamerInvalidRequest(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "http://example.test/invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.URL.Host = ""
	response, err := http.DefaultTransport.RoundTrip(request)
	if response != nil {
		response.Body.Close()
		t.Fatalf("invalid request response = %+v", response)
	}
	if err == nil || err.Error() != "http: no Host in request URL" {
		t.Fatalf("invalid request error = %v", err)
	}
}

func testConftamerBodyAndTrailers(t *testing.T, client *http.Client, serverURL string) {
	body := &conftamerCountingBody{reader: strings.NewReader("request body")}
	request, err := http.NewRequestWithContext(http.ConftamerContext(context.Background()), http.MethodPost, serverURL+"/behavior/data", body)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
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
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL+"/behavior/cancel", nil)
		if err != nil {
			t.Fatal(err)
		}
		return request
	}

	ctx, cancel := context.WithCancel(http.ConftamerContext(context.Background()))
	if err := run(http.DefaultClient, newRequest(ctx), cancel); !errors.Is(err, context.Canceled) {
		t.Fatalf("context cancellation error = %v", err)
	}
	legacy := make(chan struct{})
	legacyRequest := newRequest(http.ConftamerContext(context.Background()))
	legacyRequest.Cancel = legacy
	if err := run(http.DefaultClient, legacyRequest, func() { close(legacy) }); err == nil || !strings.Contains(err.Error(), "request canceled") {
		t.Fatalf("Request.Cancel error = %v", err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	transportRequest := newRequest(http.ConftamerContext(context.Background()))
	if err := run(&http.Client{Transport: transport}, transportRequest, func() { transport.CancelRequest(transportRequest) }); err == nil || !strings.Contains(err.Error(), "request canceled") {
		t.Fatalf("CancelRequest error = %v", err)
	}
	deadline, stop := context.WithTimeout(http.ConftamerContext(context.Background()), 50*time.Millisecond)
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
	var redirected *http.Request
	client := &http.Client{CheckRedirect: func(next *http.Request, via []*http.Request) error {
		if len(via) != 1 || next.Response == nil || next.Response.Request != via[0] {
			return errors.New("unexpected redirect chain")
		}
		redirected = next
		return nil
	}}
	request, err := http.NewRequestWithContext(http.ConftamerContext(context.Background()), http.MethodGet, serverURL+"/redirect/start", nil)
	if err != nil {
		t.Fatal(err)
	}
	request = http.ConftamerWithClientAPI(request, "example.org/redirect")
	originalContext := request.Context()
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if redirected == nil || redirected.Context() != originalContext || request.Context() != originalContext || response.Request != redirected {
		t.Fatal("redirect changed request or context identity")
	}
}

func testConftamerRetry(t *testing.T, serverURL string) {
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
		conftamerRequest(t, client, serverURL+path, http.ConftamerContext(context.Background()), false, 1)
	}
	select {
	case <-retried:
	default:
		t.Fatal("transport did not report a retry")
	}
}

func conftamerLifecycleRequest(t *testing.T, client *http.Client, target string, wantError bool) {
	t.Helper()
	request, err := http.NewRequestWithContext(http.ConftamerContext(context.Background()), http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if wantError {
		if err == nil {
			response.Body.Close()
			t.Fatal("request error = nil")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusSwitchingProtocols {
		_, _ = io.Copy(io.Discard, response.Body)
	}
}

func conftamerRequest(t *testing.T, client *http.Client, target string, ctx context.Context, direct bool, wantProtocol int) {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	originalContext := request.Context()
	var response *http.Response
	if direct {
		response, err = http.DefaultTransport.RoundTrip(request)
	} else {
		response, err = client.Do(request)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	_, readErr := io.Copy(io.Discard, response.Body)
	if readErr != nil || response.StatusCode != http.StatusNoContent || response.ProtoMajor != wantProtocol || request.Context() != originalContext || response.Request != request {
		t.Fatalf("response/read/context = %v/%v/%d/%d/%t/%t", err, readErr, response.StatusCode, response.ProtoMajor, request.Context() == originalContext, response.Request == request)
	}
}

func captureConftamerMode(t *testing.T, mode string) []conftamerCapturedRecord {
	t.Helper()
	return captureConftamerModeWithSettings(t, mode, nil)
}

func captureConftamerModeWithSettings(t *testing.T, mode string, settings map[string]string) []conftamerCapturedRecord {
	t.Helper()
	directory := t.TempDir()
	captureSettings := map[string]string{
		"CONFTAMER_EVENTS_DIR": directory,
		"CONFTAMER_CAPTURE_ID": "unit-capture",
	}
	for name, value := range settings {
		captureSettings[name] = value
	}
	runConftamerChild(t, mode, captureSettings, true)
	return readConftamerRecords(t, directory)
}

func runConftamerChild(t *testing.T, mode string, settings map[string]string, wantEnabled bool) string {
	t.Helper()
	child := newConftamerChildProcess(mode, settings)
	if err := child.command.Run(); err != nil {
		t.Fatalf("capture child failed: %v\nstdout: %s\nstderr: %s", err, child.stdout.String(), child.stderr.String())
	}
	waitConftamerChild(t, child, wantEnabled)
	return child.stdout.String()
}

func newConftamerChildProcess(mode string, settings map[string]string) *conftamerChildProcess {
	overrides := map[string]string{conftamerCaptureChild: mode}
	for name, value := range settings {
		overrides[name] = value
	}
	child := new(conftamerChildProcess)
	child.command = exec.Command(os.Args[0], "-test.run=^TestConftamerCaptureChild$", "-test.count=1")
	child.command.Env = conftamerEnvironment(overrides)
	child.command.Stdout = &child.stdout
	child.command.Stderr = &child.stderr
	return child
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

func conftamerWorkloadMarker(t *testing.T, output string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "conftamer-workload: ") {
			return line
		}
	}
	t.Fatalf("workload marker missing from %q", output)
	return ""
}

func assertConftamerParallelRecords(t *testing.T, records []conftamerCapturedRecord, captureID, processID string) {
	t.Helper()
	if len(records) != conftamerParallelRequests*4 {
		t.Fatalf("process %s records = %d, want %d", processID, len(records), conftamerParallelRequests*4)
	}
	exchanges := make(map[uint64][]string)
	contexts := make(map[uint64]bool)
	for index, record := range records {
		if record.SchemaVersion != 2 || record.CaptureID != captureID || record.ProcessID != processID || record.Seq != uint64(index+1) {
			t.Fatalf("process %s record %d envelope = %+v", processID, index+1, record)
		}
		exchanges[record.ExchangeID] = append(exchanges[record.ExchangeID], record.Kind)
		if strings.HasSuffix(record.Kind, "request") {
			if record.ContextID == nil {
				t.Fatalf("process %s exchange %d has unknown context", processID, record.ExchangeID)
			}
			contexts[*record.ContextID] = true
		}
	}
	if len(exchanges) != conftamerParallelRequests*2 || len(contexts) != conftamerParallelRequests*2 {
		t.Fatalf("process %s exchange/context counts = %d/%d, want %d/%d", processID, len(exchanges), len(contexts), conftamerParallelRequests*2, conftamerParallelRequests*2)
	}
	for id := uint64(1); id <= conftamerParallelRequests*2; id++ {
		kinds := exchanges[id]
		sort.Strings(kinds)
		pair := strings.Join(kinds, ",")
		if pair != "receive_request,send_response" && pair != "receive_response,send_request" {
			t.Fatalf("process %s exchange %d kinds = %v", processID, id, kinds)
		}
		if !contexts[id] {
			t.Fatalf("process %s is missing independently allocated context %d", processID, id)
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
	files, err := filepath.Glob(filepath.Join(directory, "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	processes := make(map[string][]conftamerCapturedRecord, len(files))
	for _, path := range files {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
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
	if os.Getenv(conftamerConfigurationChild) != "" {
		t.Fatal("parent configuration test ran as a child")
	}
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
		{name: "legacy wins over v2", env: map[string]string{"CONFTAMER_EVENTS": "/tmp/legacy", "CONFTAMER_EVENTS_DIR": t.TempDir(), "CONFTAMER_CAPTURE_ID": "unit"}, want: "CONFTAMER_EVENTS is unsupported"},
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

func TestConftamerConfigurationChild(t *testing.T) {
	if os.Getenv(conftamerConfigurationChild) == "" {
		t.Skip("helper process")
	}
}

func runConftamerConfigurationChild(t *testing.T, settings map[string]string) string {
	t.Helper()
	overrides := map[string]string{conftamerConfigurationChild: "1"}
	for name, value := range settings {
		overrides[name] = value
	}
	command := exec.Command(os.Args[0], "-test.run=^TestConftamerConfigurationChild$", "-test.count=1")
	command.Env = conftamerEnvironment(overrides)
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
