// These integration tests exercise a Go tree patched with go-inlibrary.patch.
// Run with a fresh CONFTAMER_EVENTS file; see ../../README.md.
package httpcapture

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRoundTripCapture(t *testing.T) {
	for _, tc := range []struct {
		name       string
		h2, direct bool
		method     string
	}{
		{"http1/client/get", false, false, "GET"},
		{"http1/client/empty-method", false, false, ""},
		{"http1/transport/get", false, true, "GET"},
		{"http1/transport/empty-method", false, true, ""},
		{"http2/client/get", true, false, "GET"},
		{"http2/client/empty-method", true, false, ""},
		{"http2/transport/get", true, true, "GET"},
		{"http2/transport/empty-method", true, true, ""},
		{"http1/client/post", false, false, "POST"},
		{"http1/transport/post", false, true, "POST"},
		{"http2/client/post", true, false, "POST"},
		{"http2/transport/post", true, true, "POST"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := "/capture/" + tc.name
			s := server(t, tc.h2, path, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(204)
			})
			const query = "page=1&label=%C3%A9"
			request(t, s, tc.method, path+"?"+query, tc.direct)
			method := tc.method
			if method == "" {
				method = "GET"
			}
			host := strings.TrimPrefix(strings.TrimPrefix(s.URL, "https://"), "http://")
			sent := onlyEvent(t, path, "Request sent")
			if sent.RequestID.Method != method || sent.Message["req.Method"] != method ||
				sent.RequestID.Path != path || sent.RequestID.Host != host {
				t.Errorf("incorrect outbound label: %+v", sent)
			}
			if sent.Context.ID == "" {
				t.Fatal("outbound request has no context ID")
			}
			// Client and wire hooks may both report the same response.
			assertResponses(t, path, method, "204", sent.Context.ID)
			received := onlyEvent(t, path, "Request received")
			routed := onlyEvent(t, path, "Request routed")
			reply := onlyEvent(t, path, "Response sent")
			replySource := "server.go"
			if tc.h2 {
				replySource = "h2_bundle.go"
			}
			for _, want := range []struct {
				e             event
				source, apiID string
				extra         map[string]string
			}{
				{sent, "transport.go", captureAPI, map[string]string{"req.URL.Host": host, "req.URL.RawQuery": query}},
				{received, "server.go", "", map[string]string{"req.URL.RawQuery": query}},
				{routed, "conftamer.go", "", map[string]string{"pattern": path}},
				{reply, replySource, "", map[string]string{"code": "204"}},
			} {
				assertMetadata(t, want.e, want.source, want.apiID, "")
				message := map[string]string{"req.Method": method, "req.URL.Path": path}
				maps.Copy(message, want.extra)
				if !maps.Equal(want.e.Message, message) {
					t.Errorf("%s payload = %v, want %v", want.e.Kind, want.e.Message, message)
				}
			}
			assertResponseMetadata(t, path, tc.direct)
			if received.Context.ID == "" || received.Context.ID == sent.Context.ID ||
				routed.Context.ID != received.Context.ID || reply.Context.ID != received.Context.ID ||
				routed.Message["pattern"] != path || reply.Message["code"] != "204" {
				t.Errorf("incorrect server correlation: receive=%+v route=%+v response=%+v", received, routed, reply)
			}
		})
	}
}

// Body-bearing requests are wrapped for rewinding; the second request on each
// connection exercises reuse, including the cached HTTP/2 path.
func TestBodyRequestCapture(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		for _, direct := range []bool{false, true} {
			name := fmt.Sprintf("h2=%v/direct=%v", h2, direct)
			t.Run(name, func(t *testing.T) {
				prefix := "/body/" + name + "/"
				s := server(t, h2, prefix, func(w http.ResponseWriter, r *http.Request) {
					io.Copy(io.Discard, r.Body)
					w.WriteHeader(201)
				})
				for i := range 2 {
					path := prefix + fmt.Sprint(i)
					req, err := http.NewRequest("POST", s.URL+path, strings.NewReader("payload"))
					if err != nil {
						t.Fatal(err)
					}
					resp, err := send(s.Client(), req, direct)
					if err != nil {
						t.Fatal(err)
					}
					resp.Body.Close()
					if resp.StatusCode != 201 {
						t.Fatalf("status = %d, want 201", resp.StatusCode)
					}
					if os.Getenv("CONFTAMER_EVENTS") == "" {
						continue
					}
					sent := onlyEvent(t, path, "Request sent")
					if sent.Context.ID == "" {
						t.Fatalf("missing request context: %+v", sent)
					}
					assertResponses(t, path, "POST", "201", sent.Context.ID)
				}
			})
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
				assertResponses(t, hop.path, hop.method, hop.status, contextID)
			}
		})
	}
}

// Client.Timeout makes Go fork each hop to add a deadline. Redirect hops must
// share the first hop's context ID without inheriting a finished hop's deadline.
func TestRedirectWithClientTimeout(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		t.Run(fmt.Sprint(h2), func(t *testing.T) {
			prefix := fmt.Sprintf("/timeout-redirect/%v/", h2)
			s := server(t, h2, prefix, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == prefix+"a" {
					http.Redirect(w, r, prefix+"b", 302)
					return
				}
				w.WriteHeader(204)
			})
			client := s.Client()
			client.Timeout = 10 * time.Second
			req, err := http.NewRequest("GET", s.URL+prefix+"a", nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != 204 {
				t.Fatalf("status = %d, want 204", resp.StatusCode)
			}
			if os.Getenv("CONFTAMER_EVENTS") != "" {
				first, second := onlyEvent(t, prefix+"a", "Request sent"), onlyEvent(t, prefix+"b", "Request sent")
				if first.Context.ID == "" || second.Context.ID != first.Context.ID {
					t.Errorf("redirect under Client.Timeout lost the context: %+v then %+v", first, second)
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

// Requests that Transport rejects before trying to send them are not messages.
func TestRejectedRequestsNotSent(t *testing.T) {
	for _, tc := range []struct{ name, method, url string }{
		{"relative", "GET", "/rejected/relative"},
		{"scheme", "GET", "ftp://rejected.invalid/rejected/scheme"},
		{"method", "BAD METHOD", "http://rejected.invalid/rejected/method"},
		{"host", "GET", "http:///rejected/host"},
	} {
		for _, direct := range []bool{false, true} {
			u, err := url.Parse(fmt.Sprintf("%s/%v", tc.url, direct))
			if err != nil {
				t.Fatal(err)
			}
			// NewRequest would reject the invalid method itself.
			req := &http.Request{Method: tc.method, URL: u, Header: http.Header{}}
			transport := &http.Transport{}
			_, err = send(&http.Client{Transport: transport}, req, direct)
			if err == nil {
				t.Fatalf("%s: request unexpectedly succeeded", tc.name)
			}
			if got := events(t, u.Path, "Request sent"); len(got) != 0 {
				t.Errorf("%s: rejected request logged as sent: %+v", tc.name, got)
			}
		}
	}
}

func TestRejectedHeadersNotSent(t *testing.T) {
	for _, header := range []struct{ name, key, value string }{
		{"name", "Bad\nName", "value"},
		{"value", "X-Test", "bad\nvalue"},
	} {
		for _, direct := range []bool{false, true} {
			name := fmt.Sprintf("%s/direct=%v", header.name, direct)
			t.Run(name, func(t *testing.T) {
				path := "/rejected-header/" + name
				req, err := http.NewRequest("GET", "http://rejected.invalid"+path, nil)
				if err != nil {
					t.Fatal(err)
				}
				req.Header[header.key] = []string{header.value}
				var dials atomic.Int32
				transport := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
					dials.Add(1)
					return nil, fmt.Errorf("unexpected dial for invalid header")
				}}
				t.Cleanup(transport.CloseIdleConnections)
				resp, err := send(&http.Client{Transport: transport}, req, direct)
				if resp != nil {
					resp.Body.Close()
				}
				if err == nil {
					t.Error("invalid header unexpectedly accepted")
				}
				if got := dials.Load(); got != 0 {
					t.Errorf("rejected header caused %d dial attempts", got)
				}
				if got := events(t, path, "Request sent"); len(got) != 0 {
					t.Errorf("rejected header logged as sent: %+v", got)
				}
			})
		}
	}
}

func TestAlternateProtocolSendAttempt(t *testing.T) {
	for _, direct := range []bool{false, true} {
		t.Run(fmt.Sprint(direct), func(t *testing.T) {
			path := fmt.Sprintf("/alternate-protocol/%v", direct)
			transport := &http.Transport{}
			t.Cleanup(transport.CloseIdleConnections)
			transport.RegisterProtocol("capture", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 204, Header: http.Header{}, Body: http.NoBody, Request: r}, nil
			}))
			req, err := http.NewRequest("GET", "capture://alternate.invalid"+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := send(&http.Client{Transport: transport}, req, direct)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != 204 || resp.Request != req {
				t.Fatalf("alternate protocol lost response status or caller identity: %+v", resp)
			}
			sent := onlyEvent(t, path, "Request sent")
			if sent.RequestID.Method != "GET" || sent.RequestID.Host != "alternate.invalid" || sent.RequestID.Path != path {
				t.Errorf("incorrect alternate-protocol send label: %+v", sent)
			}
			assertMetadata(t, sent, "transport.go", captureAPI, "")
		})
	}
}

func TestCachedHTTP2SendAttempt(t *testing.T) {
	for _, direct := range []bool{false, true} {
		t.Run(fmt.Sprint(direct), func(t *testing.T) {
			prefix := fmt.Sprintf("/cached-http2/%v/", direct)
			s := server(t, true, prefix, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			connections := make(chan httptrace.GotConnInfo, 2)
			ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
				select {
				case connections <- info:
				default:
					t.Error("unexpected extra connection acquisition")
				}
			}})
			prime, err := http.NewRequestWithContext(ctx, "GET", s.URL+prefix+"prime", nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := send(s.Client(), prime, direct)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.ProtoMajor != 2 {
				t.Fatalf("priming request used %s, want HTTP/2", resp.Proto)
			}
			var first httptrace.GotConnInfo
			select {
			case first = <-connections:
			default:
				t.Fatal("missing priming connection trace")
			}
			path := prefix + "bad-method"
			req, err := http.NewRequestWithContext(ctx, "GET", s.URL+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			// HTTP/1 rejects this before a send, but the cached HTTP/2 path may try.
			req.Method = "BAD METHOD"
			resp, err = send(s.Client(), req, direct)
			if resp != nil {
				resp.Body.Close()
			}
			t.Logf("cached HTTP/2 attempt: response=%v, error=%v", resp != nil, err)
			// A send-attempt event alone would not prove the cached path ran.
			select {
			case reused := <-connections:
				if !reused.Reused || first.Conn == nil || reused.Conn != first.Conn {
					t.Fatalf("did not reuse the primed HTTP/2 connection: first=%+v next=%+v", first, reused)
				}
			default:
				t.Fatal("bad-method request did not acquire the cached HTTP/2 connection")
			}
			sent := onlyEvent(t, path, "Request sent")
			if sent.RequestID.Method != "BAD METHOD" || sent.Message["req.Method"] != "BAD METHOD" || sent.RequestID.Path != path {
				t.Errorf("incorrect cached HTTP/2 attempt label: %+v", sent)
			}
			assertMetadata(t, sent, "transport.go", captureAPI, "")
		})
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
				resp, err := send(s.Client(), req, direct)
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
	assertBodyInterrupted(t, resp.Body, cancel)
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
	cmd := exec.Command(executable, "-test.run=^Test(RequestOwnership|ClientDoCancelRequest|ClientDoLegacyCancel|CallerRequestIdentity|CheckRedirectRequestIdentity|ClientNativeCopies|TransportWrapperCopies|RoundTripperReturnsRequest|RedirectWithClientTimeout|BodyRequestCapture)$", "-test.count=1")
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
