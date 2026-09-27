package httpcapture

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"testing"
	"time"
)

func TestClientDoCancelRequest(t *testing.T) {
	s := server(t, false, "/client-cancel", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", s.URL+"/client-cancel", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// Cancel after RoundTrip has returned, while the response body is in flight.
	s.Client().Transport.(*http.Transport).CancelRequest(req)
	done := make(chan error, 1)
	go func() { _, err := io.ReadAll(resp.Body); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("expected cancellation to interrupt the response body")
		}
	case <-time.After(3 * time.Second):
		cancel()
		<-done
		t.Fatal("CancelRequest did not recognize the original Client.Do request")
	}
}

func TestCallerRequestIdentity(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		for _, direct := range []bool{false, true} {
			name := fmt.Sprintf("h2=%v/direct=%v", h2, direct)
			t.Run(name, func(t *testing.T) {
				prefix := "/identity/" + name + "/"
				s := server(t, h2, prefix, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
				// The second HTTP/2 request exercises the cached alternate-protocol path.
				for i := range 2 {
					path := prefix + fmt.Sprint(i)
					reused := false
					ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
						GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused },
					})
					req, err := http.NewRequestWithContext(ctx, "GET", s.URL+path, nil)
					if err != nil {
						t.Fatal(err)
					}
					var resp *http.Response
					if direct {
						resp, err = s.Client().Transport.RoundTrip(req)
					} else {
						resp, err = s.Client().Do(req)
					}
					if err != nil {
						t.Fatal(err)
					}
					resp.Body.Close()
					if h2 && resp.ProtoMajor != 2 {
						t.Fatalf("expected HTTP/2, got %s", resp.Proto)
					}
					if i == 1 && !reused {
						t.Fatal("second request did not reuse the connection")
					}
					if resp.Request != req {
						t.Errorf("round trip %d: Response.Request is not the caller's request", i)
					}
					if req.Context() != ctx {
						t.Error("tracing changed the original request context")
					}
					if os.Getenv("CONFTAMER_EVENTS") != "" {
						sent := onlyEvent(t, path, "Request sent")
						received := events(t, path, "Response received")
						if sent.Context.ID == "" || len(received) == 0 {
							t.Fatalf("missing request/response correlation: sent=%+v received=%+v", sent, received)
						}
						for _, e := range received {
							if e.Context.ID != sent.Context.ID || e.Message["resp.StatusCode"] != "204" {
								t.Errorf("restoring identity broke response capture: %+v", e)
							}
						}
					}
				}
			})
		}
	}
}

func TestCheckRedirectRequestIdentity(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		for _, stop := range []bool{false, true} {
			name := fmt.Sprintf("h2=%v/stop=%v", h2, stop)
			t.Run(name, func(t *testing.T) {
				prefix := "/redirect-identity/" + name + "/"
				s := server(t, h2, prefix, func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case prefix + "a":
						http.Redirect(w, r, prefix+"b", 302)
					case prefix + "b":
						http.Redirect(w, r, prefix+"c", 302)
					default:
						w.WriteHeader(204)
					}
				})
				req, err := http.NewRequest("GET", s.URL+prefix+"a", nil)
				if err != nil {
					t.Fatal(err)
				}
				originalContext := req.Context()
				wantRequests := []*http.Request{req}
				client := s.Client()
				client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
					if len(via) != len(wantRequests) {
						return fmt.Errorf("redirect history length = %d, want %d", len(via), len(wantRequests))
					}
					for i := range via {
						if via[i] != wantRequests[i] {
							t.Errorf("via[%d] is a tracing copy, not the original hop", i)
						}
					}
					if next.Response == nil || next.Response.Request != wantRequests[len(wantRequests)-1] {
						t.Error("redirect Response.Request is not the preceding hop's request")
					}
					if stop && len(via) == 2 {
						return http.ErrUseLastResponse
					}
					wantRequests = append(wantRequests, next)
					return nil
				}
				resp, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				wantStatus := 204
				if stop {
					wantStatus = 302
				}
				if resp.StatusCode != wantStatus {
					t.Errorf("status = %d, want %d", resp.StatusCode, wantStatus)
				}
				if resp.Request != wantRequests[len(wantRequests)-1] {
					t.Error("final Response.Request is not the last sent hop's request")
				}
				if req.Context() != originalContext {
					t.Error("redirect tracing changed the original request context")
				}
			})
		}
	}
}

// Only instrumentation-owned copies should be unwrapped. Go's own copies for
// headers, credentials, and deadlines must still expose their adjusted fields.
func TestClientNativeCopies(t *testing.T) {
	for _, name := range []string{"nil-header", "auth", "timeout"} {
		t.Run(name, func(t *testing.T) {
			path := "/native-copy/" + name
			s := server(t, false, path, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
			req, err := http.NewRequest("GET", s.URL+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			client := s.Client()
			switch name {
			case "nil-header":
				req.Header = nil
			case "auth":
				req.URL.User = url.UserPassword("user", "password")
			case "timeout":
				client.Timeout = 5 * time.Second
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.Request == nil || resp.Request == req {
				t.Fatal("Go's native send copy was discarded")
			}
			switch name {
			case "nil-header":
				if req.Header != nil || resp.Request.Header == nil {
					t.Error("header initialization did not stay on Go's native copy")
				}
			case "auth":
				user, password, ok := resp.Request.BasicAuth()
				if !ok || user != "user" || password != "password" || req.Header.Get("Authorization") != "" {
					t.Error("basic authentication did not stay on Go's native copy")
				}
			case "timeout":
				if _, ok := resp.Request.Context().Deadline(); !ok {
					t.Error("Go's client deadline was discarded")
				}
				if _, ok := req.Context().Deadline(); ok {
					t.Error("Go's client deadline leaked onto the caller's request")
				}
			}
		})
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// An application transport may copy the request it receives. Inherited private
// tracing metadata must not cause that application's copy to be unwrapped.
func TestTransportWrapperCopies(t *testing.T) {
	for _, mode := range []string{"with-context", "clone", "shallow"} {
		t.Run(mode, func(t *testing.T) {
			path := "/wrapper-copy/" + mode
			s := server(t, false, path, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
			backend := s.Client().Transport
			var forwarded *http.Request
			client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				switch mode {
				case "with-context":
					forwarded = r.WithContext(r.Context())
				case "clone":
					forwarded = r.Clone(r.Context())
				case "shallow":
					copy := *r
					forwarded = &copy
				}
				return backend.RoundTrip(forwarded)
			})}
			req, err := http.NewRequest("GET", s.URL+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if forwarded == req || resp.Request != forwarded {
				t.Error("instrumentation discarded the application transport's request copy")
			}
		})
	}
}
