package httpcapture

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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
	resp, err := send(s.Client(), req, direct)
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

func send(c *http.Client, r *http.Request, direct bool) (*http.Response, error) {
	if direct {
		return c.Transport.RoundTrip(r)
	}
	return c.Do(r)
}

func assertResponses(t *testing.T, path, method, status, contextID string) {
	t.Helper()
	got := events(t, path, "Response received")
	if len(got) == 0 {
		t.Fatal("missing Response received")
	}
	for _, e := range got {
		if e.Context.ID != contextID || e.Message["req.Method"] != method ||
			e.Message["resp.StatusCode"] != status {
			t.Errorf("wrong response evidence: %+v", e)
		}
	}
}

func assertBodyInterrupted(t *testing.T, body io.Reader, unblock func()) {
	t.Helper()
	done := make(chan error, 1)
	go func() { _, err := io.ReadAll(body); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("expected cancellation to interrupt the response body")
		}
	case <-time.After(5 * time.Second):
		unblock()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("response body remained blocked after cleanup")
		}
		t.Fatal("cancellation did not interrupt the response body")
	}
}
