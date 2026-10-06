package httpcapture

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

const captureAPI = "conftamer-contexttrack-httpcapture"

// Client calls report both wire and client hooks; direct transport reports wire only.
func assertResponseMetadata(t *testing.T, path string, direct bool) {
	t.Helper()
	wantSources := map[string]int{"conftamer.go": 1}
	if !direct {
		wantSources["client.go"] = 1
	}
	sources := make(map[string]int)
	for _, e := range events(t, path, "Response received") {
		source, apiID := filepath.Base(e.File), ""
		if source == "client.go" {
			apiID = captureAPI
		}
		assertMetadata(t, e, source, apiID, "")
		sources[source]++
		// assertResponses checks the three expected fields; reject extras too.
		if len(e.Message) != 3 {
			t.Errorf("unexpected response fields: %v", e.Message)
		}
	}
	if !maps.Equal(sources, wantSources) {
		t.Errorf("response hook sources = %v, want %v", sources, wantSources)
	}
}

type metadataHandler struct{}

func (metadataHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(202)
}

func namedMetadataHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(202)
}

func TestHandlerMetadata(t *testing.T) {
	for _, handler := range []struct {
		name string
		h    http.Handler
		want string
	}{
		{"value", metadataHandler{}, "httpcapture.metadataHandler"},
		// Pointer handlers report the underlying type, without a pointer prefix.
		{"pointer", &metadataHandler{}, "httpcapture.metadataHandler"},
		{"named-function", http.HandlerFunc(namedMetadataHandler), "conftamer-contexttrack-httpcapture.namedMetadataHandler"},
	} {
		for _, protocol := range []struct {
			name string
			h2   bool
		}{{"http1", false}, {"http2", true}} {
			t.Run(handler.name+"/"+protocol.name, func(t *testing.T) {
				path := "/metadata/handler/" + handler.name + "/" + protocol.name
				s := httptest.NewUnstartedServer(handler.h)
				s.EnableHTTP2 = protocol.h2
				if protocol.h2 {
					s.StartTLS()
				} else {
					s.Start()
				}
				t.Cleanup(s.Close)
				request(t, s, "GET", path, false)
				received := onlyEvent(t, path, "Request received")
				assertMetadata(t, received, "server.go", captureAPI, handler.want)
				reply := onlyEvent(t, path, "Response sent")
				if reply.ApiId != "" || reply.Handler != "" {
					t.Errorf("response fabricated handler attribution: %+v", reply)
				}
			})
		}
	}
}

func metadataGenericSend[T http.RoundTripper](transport T, req *http.Request) (*http.Response, error) {
	return transport.RoundTrip(req)
}

func TestGenericCallerMetadata(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		name := "http1"
		if h2 {
			name = "http2"
		}
		t.Run(name, func(t *testing.T) {
			path := "/metadata/generic/" + name
			s := server(t, h2, path, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
			req, err := http.NewRequest("GET", s.URL+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := metadataGenericSend(s.Client().Transport.(*http.Transport), req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			// API association must not depend on compiler-specific generic names.
			assertMetadata(t, onlyEvent(t, path, "Request sent"), "transport.go", captureAPI, "")
			assertResponseMetadata(t, path, true)
		})
	}
}

// Custom transports may change the private copy, but not the caller's hop labels.
func TestClientResponseKeepsHopLabels(t *testing.T) {
	const path = "/metadata/client-hop"
	client := &http.Client{Transport: roundTripperFunc(func(sent *http.Request) (*http.Response, error) {
		sent.Method = "PATCH"
		return &http.Response{StatusCode: 204, Body: http.NoBody, Request: sent}, nil
	})}
	req, err := http.NewRequest("POST", "http://metadata.invalid"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if req.Method != "POST" || resp.Request != req {
		t.Fatal("transport changes leaked onto the caller's request")
	}
	response := onlyEvent(t, path, "Response received")
	assertMetadata(t, response, "client.go", captureAPI, "")
	if response.Message["req.Method"] != "POST" || response.Message["resp.StatusCode"] != "204" {
		t.Errorf("client response lost original-hop labels: %+v", response)
	}
}

func assertMetadata(t *testing.T, e event, source, apiID, handler string) {
	t.Helper()
	if filepath.Base(e.File) != source || e.Line <= 0 {
		t.Errorf("source = %s:%d, want positive line in %s", e.File, e.Line, source)
	}
	if e.ApiId != apiID || e.Handler != handler {
		t.Errorf("attribution = (%q, %q), want (%q, %q)", e.ApiId, e.Handler, apiID, handler)
	}
	if e.PID != os.Getpid() || e.GoroutineID <= 0 || e.ThreadID != 0 {
		t.Errorf("incorrect process/debug metadata: %+v", e)
	}
	if e.Context.ID == "" || e.Context.Source != "req.Context()" ||
		e.Context.Type != "context.Context" || e.Context.Error != "" {
		t.Errorf("incorrect context metadata: %+v", e)
	}
}
