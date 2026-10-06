package httpcapture

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	captureLoggingChild = "CONTEXTTRACK_CAPTURE_LOGGING_CHILD"
	captureLoggingFile  = "CONTEXTTRACK_CAPTURE_LOGGING_FILE"
)

func checkCapture(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func requireDevFull(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux /dev/full")
	}
	if info, err := os.Stat("/dev/full"); err != nil || info.Mode()&os.ModeCharDevice == 0 {
		t.Skip("requires /dev/full character device")
	}
}

func TestCaptureWriteFailure(t *testing.T) {
	requireDevFull(t)
	capture := "/dev/full"
	_, stderr := runCaptureLoggingChildMode(t, &capture, "1", "")
	startup, warning, _ := strings.Cut(stderr, "\n")
	pattern := regexp.MustCompile(`^conftamer: capture write failed for "/dev/full": .+; capture may be incomplete or invalid\n$`)
	if startup != `conftamer: enabled — writing "/dev/full"` || !pattern.MatchString(warning) {
		t.Errorf("want enabled diagnostic and exactly one capture-write warning, got:\n%s", stderr)
	}
	// Never read /dev/full as a capture: it is an unbounded device, not JSONL.
}

func TestCaptureStderrFileLifetime(t *testing.T) {
	requireDevFull(t)
	for _, mode := range []string{"closed-stderr", "redirected-stderr"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "application-file.txt")
			const original = "application data\n"
			checkCapture(t, os.WriteFile(path, []byte(original), 0o600))
			capture := "/dev/full"
			stdout, stderr := runCaptureLoggingChildMode(t, &capture, mode, path)
			if !strings.Contains(stdout, "HTTP completed with 204\n") {
				t.Fatalf("missing successful HTTP completion: %s", stdout)
			}
			if want := "conftamer: enabled — writing \"/dev/full\"\n"; stderr != want {
				t.Errorf("old stderr output = %q, want only startup diagnostic %q", stderr, want)
			}
			data, err := os.ReadFile(path)
			checkCapture(t, err)
			if mode == "closed-stderr" {
				if !strings.Contains(stdout, "application file reused fd 2\n") {
					t.Fatalf("child did not confirm descriptor reuse: %s", stdout)
				}
				if string(data) != original {
					t.Errorf("closed stderr contaminated unrelated application file: %q", data)
				}
			} else {
				if !strings.HasPrefix(string(data), original) {
					t.Fatalf("redirected stderr overwrote application data: %q", data)
				}
				warning := strings.TrimPrefix(string(data), original)
				pattern := regexp.MustCompile(`^conftamer: capture write failed for "/dev/full": .+; capture may be incomplete or invalid\n$`)
				if !pattern.MatchString(warning) {
					t.Errorf("want exactly one warning in reassigned stderr file, got %q", warning)
				}
			}
		})
	}
}

func TestCaptureLoggingControls(t *testing.T) {
	for _, name := range []string{"healthy", "unset", "empty", "open-failure"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "events.jsonl")
			var capture *string
			if name != "unset" {
				capture = &path
			}
			switch name {
			case "empty":
				path = ""
			case "open-failure":
				path = filepath.Join(filepath.Dir(path), "missing", "events.jsonl")
			}
			_, stderr := runCaptureLoggingChildMode(t, capture, "1", "")
			switch name {
			case "healthy":
				if want := fmt.Sprintf("conftamer: enabled — writing %q\n", *capture); stderr != want {
					t.Errorf("stderr = %q, want %q", stderr, want)
				}
				kinds := make(map[string]int)
				for _, e := range readCapture(t, *capture) {
					if e.Message["req.URL.Path"] != "/capture-logging" || e.Context.ID == "" {
						t.Fatalf("incorrect healthy capture event: %+v", e)
					}
					kinds[e.Kind]++
				}
				want := map[string]int{
					"Request sent": 17, "Request received": 17, "Request routed": 17,
					"Response sent": 17, "Response received": 34,
				}
				if !maps.Equal(kinds, want) {
					t.Errorf("event counts = %v, want %v", kinds, want)
				}
			case "open-failure":
				want := fmt.Sprintf("conftamer: CONFTAMER_EVENTS=%q set but open failed: ", *capture)
				if !strings.HasPrefix(stderr, want) || strings.Count(stderr, "\n") != 1 {
					t.Errorf("want only the open-failure diagnostic, got: %s", stderr)
				}
				if _, err := os.Stat(filepath.Dir(*capture)); !os.IsNotExist(err) {
					t.Errorf("capture unexpectedly created its parent directory: %v", err)
				}
			default:
				if stderr != "" {
					t.Errorf("disabled capture was not silent: %s", stderr)
				}
			}
		})
	}
}

func captureCommand(t *testing.T, capture *string, mode, path string) *exec.Cmd {
	t.Helper()
	executable, err := os.Executable()
	checkCapture(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestCaptureLoggingChild$", "-test.count=1", "-test.timeout=10s")
	t.Cleanup(cancel)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if key != "CONFTAMER_EVENTS" && key != captureLoggingChild && key != captureLoggingFile {
			cmd.Env = append(cmd.Env, item)
		}
	}
	cmd.Env = append(cmd.Env, captureLoggingChild+"="+mode, captureLoggingFile+"="+path)
	if capture != nil {
		cmd.Env = append(cmd.Env, "CONFTAMER_EVENTS="+*capture)
	}
	return cmd
}

func runCaptureLoggingChildMode(t *testing.T, capture *string, mode, path string) (string, string) {
	t.Helper()
	stderr, err := os.CreateTemp(t.TempDir(), "stderr")
	checkCapture(t, err)
	defer stderr.Close()
	if path == "" {
		path = stderr.Name() // The child can observe asynchronous warning delivery.
	}
	cmd := captureCommand(t, capture, mode, path)
	var stdout bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, stderr
	runErr := cmd.Run()
	data, err := os.ReadFile(stderr.Name())
	checkCapture(t, err)
	if runErr != nil {
		t.Fatalf("capture-logging subprocess failed: %v\nstdout: %s\nstderr: %s", runErr, &stdout, data)
	}
	return stdout.String(), string(data)
}

// The logger initializes once per process, so each destination needs a child.
func TestCaptureLoggingChild(t *testing.T) {
	mode := os.Getenv(captureLoggingChild)
	switch mode {
	case "1", "broken-stderr", "closed-stderr", "redirected-stderr":
	default:
		t.Skip("subprocess helper")
	}
	if mode == "broken-stderr" {
		fmt.Fprintln(os.Stdout, "ready")
		var release [1]byte
		if _, err := io.ReadFull(os.Stdin, release[:]); err != nil || release[0] != '\n' {
			t.Fatalf("HTTP release handshake failed: %v", err)
		}
	}
	if mode == "closed-stderr" || mode == "redirected-stderr" {
		originalStderr := os.Stderr
		if mode == "closed-stderr" {
			checkCapture(t, originalStderr.Close())
		}
		file, err := os.OpenFile(os.Getenv(captureLoggingFile), os.O_WRONLY|os.O_APPEND, 0o600)
		checkCapture(t, err)
		defer file.Close()
		if mode == "closed-stderr" {
			if fd := file.Fd(); fd != 2 {
				t.Fatalf("application file descriptor = %d, want reused fd 2", fd)
			}
			fmt.Fprintln(os.Stdout, "application file reused fd 2")
		} else {
			os.Stderr = file
			defer func() { os.Stderr = originalStderr }()
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/capture-logging", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	s := httptest.NewServer(mux)
	defer s.Close()
	client := s.Client()
	client.Timeout = 5 * time.Second
	defer client.CloseIdleConnections()
	doRequest := func() {
		resp, err := client.Get(s.URL + "/capture-logging")
		if err != nil {
			t.Error(err)
			return
		}
		resp.Body.Close()
		if resp.StatusCode != 204 {
			t.Errorf("HTTP status = %d, want 204", resp.StatusCode)
		}
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(doRequest)
	}
	wg.Wait()
	doRequest() // HTTP must still work after the concurrent failures.
	if os.Getenv("CONFTAMER_EVENTS") == "/dev/full" && (mode == "1" || mode == "redirected-stderr") {
		// Wait for an observable condition, not a fixed scheduling delay. This
		// keeps the captured stderr open until the worker has delivered its warning.
		deadline := time.Now().Add(5 * time.Second)
		for {
			data, err := os.ReadFile(os.Getenv(captureLoggingFile))
			checkCapture(t, err)
			if bytes.Contains(data, []byte("; capture may be incomplete or invalid\n")) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("warning was not delivered before child exit")
			}
			runtime.Gosched()
		}
	}
	if mode != "1" && !t.Failed() {
		fmt.Fprintln(os.Stdout, "HTTP completed with 204")
	}
}
