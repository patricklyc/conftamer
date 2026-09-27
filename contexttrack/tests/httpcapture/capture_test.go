// These integration tests exercise a Go tree patched with go-inlibrary.patch.
// Run with a fresh CONFTAMER_EVENTS file; see ../../README.md.
package httpcapture

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

type event struct {
	Kind    string            `json:"kind"`
	PID     int               `json:"pid"`
	Message map[string]string `json:"message"`
	Context struct {
		ID    string `json:"context_id"`
		Error string `json:"error"`
	} `json:"context"`
	RequestID struct {
		Method string `json:"method"`
		Host   string `json:"host"`
		Path   string `json:"path"`
	} `json:"request_id"`
}

func events(t *testing.T, path, kind string) []event {
	t.Helper()
	capture := os.Getenv("CONFTAMER_EVENTS")
	if capture == "" {
		t.Fatal("set CONFTAMER_EVENTS to a fresh file before running capture tests")
	}
	f, err := os.Open(capture)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var found []event
	scanner := bufio.NewScanner(f)
	for line := 1; scanner.Scan(); line++ {
		var e event
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			t.Fatalf("%s:%d: %v", capture, line, err)
		}
		if e.PID == os.Getpid() && e.Kind == kind && e.Message["req.URL.Path"] == path {
			found = append(found, e)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return found
}

func onlyEvent(t *testing.T, path, kind string) event {
	t.Helper()
	got := events(t, path, kind)
	if len(got) != 1 {
		t.Fatalf("%s %s: want one event, got %d: %+v", kind, path, len(got), got)
	}
	return got[0]
}

func server(t *testing.T, h2 bool, pattern string, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(pattern, h)
	s := httptest.NewUnstartedServer(mux)
	s.EnableHTTP2 = h2
	if h2 {
		s.StartTLS()
	} else {
		s.Start()
	}
	t.Cleanup(s.Close)
	return s
}

func request(t *testing.T, s *httptest.Server, method, path string, direct bool) *http.Response {
	t.Helper()
	req, err := http.NewRequest("GET", s.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Assign after construction to exercise Go's empty-method-means-GET rule.
	req.Method = method
	var resp *http.Response
	if direct {
		resp, err = s.Client().Transport.RoundTrip(req)
	} else {
		resp, err = s.Client().Do(req)
	}
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if s.EnableHTTP2 && resp.ProtoMajor != 2 {
		t.Fatalf("expected HTTP/2, got %s", resp.Proto)
	}
	return resp
}

func TestRoundTripCapture(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		for _, direct := range []bool{false, true} {
			for _, method := range []string{"GET", ""} {
				name := fmt.Sprintf("h2=%v/direct=%v/method=%s", h2, direct, method)
				t.Run(name, func(t *testing.T) {
					path := "/capture/" + name
					s := server(t, h2, path, func(w http.ResponseWriter, r *http.Request) {
						w.WriteHeader(204)
					})
					request(t, s, method, path, direct)
					sent := onlyEvent(t, path, "Request sent")
					if sent.RequestID.Method != "GET" || sent.Message["req.Method"] != "GET" ||
						sent.RequestID.Path != path || sent.RequestID.Host != strings.TrimPrefix(strings.TrimPrefix(s.URL, "https://"), "http://") {
						t.Errorf("incorrect outbound label: %+v", sent)
					}
					if sent.Context.ID == "" {
						t.Fatal("outbound request has no context ID")
					}
					responses := events(t, path, "Response received")
					if len(responses) == 0 {
						t.Fatal("missing Response received")
					}
					// Client and wire hooks may both report the same response.
					for _, response := range responses {
						if response.Context.ID != sent.Context.ID || response.Message["req.Method"] != "GET" || response.Message["resp.StatusCode"] != "204" {
							t.Errorf("response not labeled/correlated with its request: %+v; request: %+v", response, sent)
						}
					}
					received := onlyEvent(t, path, "Request received")
					routed := onlyEvent(t, path, "Request routed")
					reply := onlyEvent(t, path, "Response sent")
					if received.Context.ID == "" || received.Context.ID == sent.Context.ID ||
						routed.Context.ID != received.Context.ID || reply.Context.ID != received.Context.ID ||
						routed.Message["pattern"] != path || reply.Message["code"] != "204" {
						t.Errorf("incorrect server correlation: receive=%+v route=%+v response=%+v", received, routed, reply)
					}
				})
			}
		}
	}
}

func TestHTTP2ResponseStatus(t *testing.T) {
	for _, tc := range []struct {
		name string
		code string
		h    http.HandlerFunc
	}{
		{"empty", "200", func(http.ResponseWriter, *http.Request) {}},
		{"flush", "200", func(w http.ResponseWriter, _ *http.Request) { w.(http.Flusher).Flush() }},
		{"write", "200", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") }},
		{"repeated", "204", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204); w.WriteHeader(500) }},
		{"invalid", "204", func(w http.ResponseWriter, _ *http.Request) {
			defer func() {
				if recover() != nil {
					w.WriteHeader(204)
				}
			}()
			w.WriteHeader(42)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := "/status/" + tc.name
			s := server(t, true, path, tc.h)
			resp := request(t, s, "GET", path, false)
			if fmt.Sprint(resp.StatusCode) != tc.code {
				t.Fatalf("wire status = %d, want %s", resp.StatusCode, tc.code)
			}
			if got := onlyEvent(t, path, "Response sent"); got.Message["code"] != tc.code {
				t.Errorf("logged an unaccepted status: %+v", got)
			}
		})
	}
}

func TestRedirectLabels(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		t.Run(fmt.Sprint(h2), func(t *testing.T) {
			prefix := fmt.Sprintf("/redirect/%v/", h2)
			start, final := prefix+"start", prefix+"final"
			s := server(t, h2, prefix, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == start {
					http.Redirect(w, r, final, 302)
					return
				}
				w.WriteHeader(204)
			})
			request(t, s, "POST", start, false)
			var contextID string
			for _, hop := range []struct{ path, method, status string }{{start, "POST", "302"}, {final, "GET", "204"}} {
				sent := onlyEvent(t, hop.path, "Request sent")
				if contextID == "" {
					contextID = sent.Context.ID
				}
				if contextID == "" || sent.Context.ID != contextID {
					t.Errorf("redirect lost the original context: %+v", sent)
				}
				got := events(t, hop.path, "Response received")
				if len(got) == 0 {
					t.Fatalf("missing response for %s", hop.path)
				}
				for _, e := range got {
					if e.Context.ID != contextID || e.Message["req.Method"] != hop.method || e.Message["resp.StatusCode"] != hop.status {
						t.Errorf("wrong redirect hop label: %+v, want %+v", e, hop)
					}
				}
			}
		})
	}
}

func TestInheritedContext(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		t.Run(fmt.Sprint(h2), func(t *testing.T) {
			in, out := fmt.Sprintf("/inherited/%v/in", h2), fmt.Sprintf("/inherited/%v/out", h2)
			downstream := server(t, h2, out, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
			upstream := server(t, h2, in, func(w http.ResponseWriter, r *http.Request) {
				ctx, cancel := context.WithCancel(r.Context())
				defer cancel()
				req, err := http.NewRequestWithContext(ctx, "GET", downstream.URL+out, nil)
				if err != nil {
					t.Error(err)
					w.WriteHeader(500)
					return
				}
				resp, err := downstream.Client().Transport.RoundTrip(req)
				if err != nil {
					t.Error(err)
					w.WriteHeader(500)
					return
				}
				resp.Body.Close()
				w.WriteHeader(204)
			})
			request(t, upstream, "GET", in, false)
			received, sent := onlyEvent(t, in, "Request received"), onlyEvent(t, out, "Request sent")
			if received.Context.ID == "" || sent.Context.ID != received.Context.ID {
				t.Errorf("derived context lost influence group: receive=%+v send=%+v", received, sent)
			}
		})
	}
}

func TestUnstampedContext(t *testing.T) {
	const path = "/unstamped/7"
	mux := http.NewServeMux()
	mux.HandleFunc("/unstamped/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	req := httptest.NewRequest("GET", path, nil)
	for range 2 {
		mux.ServeHTTP(httptest.NewRecorder(), req)
	}
	got := events(t, path, "Request routed")
	if len(got) != 2 {
		t.Fatalf("want two routed events, got %+v", got)
	}
	for _, e := range got {
		if e.Context.ID != "" || e.Context.Error == "" {
			t.Errorf("fabricated correlation for unstamped context: %+v", e)
		}
	}
}

// This test also runs in a tracing-disabled subprocess. Run with -race to
// catch writes to caller-owned requests while another goroutine reads them.
func TestRequestOwnership(t *testing.T) {
	for _, direct := range []bool{false, true} {
		t.Run(fmt.Sprint(direct), func(t *testing.T) {
			s := server(t, false, "/ownership", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
			req, err := http.NewRequest("GET", s.URL+"/ownership", nil)
			if err != nil {
				t.Fatal(err)
			}
			original := req.Context()
			var wg sync.WaitGroup
			done := make(chan struct{})
			wg.Go(func() {
				for {
					select {
					case <-done:
						return
					default:
						_ = req.Context().Err()
					}
				}
			})
			for range 8 {
				var resp *http.Response
				if direct {
					resp, err = s.Client().Transport.RoundTrip(req)
				} else {
					resp, err = s.Client().Do(req)
				}
				if err != nil {
					t.Error(err)
					break
				}
				resp.Body.Close()
			}
			close(done)
			wg.Wait()
			if req.Context() != original {
				t.Error("tracing modified the caller-owned request context")
			}
		})
	}
}

func TestTransportCancelRequest(t *testing.T) {
	s := server(t, false, "/cancel", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", s.URL+"/cancel", nil)
	if err != nil {
		t.Fatal(err)
	}
	transport := s.Client().Transport.(*http.Transport)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	transport.CancelRequest(req)
	done := make(chan error, 1)
	go func() { _, err := io.ReadAll(resp.Body); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("expected cancellation to interrupt the response body")
		}
	case <-time.After(5 * time.Second):
		cancel()
		<-done
		t.Fatal("CancelRequest no longer recognizes the original request")
	}
}

func TestTracingDisabled(t *testing.T) {
	capture := os.Getenv("CONFTAMER_EVENTS")
	before, err := os.Stat(capture)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^Test(RequestOwnership|ClientDoCancelRequest|CallerRequestIdentity|CheckRedirectRequestIdentity|ClientNativeCopies|TransportWrapperCopies)$", "-test.count=1")
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "CONFTAMER_EVENTS=") {
			cmd.Env = append(cmd.Env, item)
		}
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("tracing-disabled requests failed: %v\n%s", err, output)
	}
	if strings.Contains(string(output), "conftamer:") {
		t.Errorf("unexpected tracing diagnostic with CONFTAMER_EVENTS unset: %s", output)
	}
	after, err := os.Stat(capture)
	if err != nil {
		t.Fatal(err)
	}
	if before.Size() != after.Size() {
		t.Error("tracing-disabled subprocess appended events")
	}
}
