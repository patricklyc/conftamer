package httpcapture

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCaptureBrokenStderr(t *testing.T) { captureStderrPipe(t, false) }
func TestCaptureFullStderr(t *testing.T)   { captureStderrPipe(t, true) }

func captureStderrPipe(t *testing.T, full bool) {
	t.Helper()
	requireDevFull(t)
	for _, capture := range []string{"/dev/null", "/dev/full"} {
		t.Run(filepath.Base(capture), func(t *testing.T) {
			cmd := captureCommand(t, &capture, "pipe-stderr", "")
			reader, writer, err := os.Pipe()
			checkCapture(t, err)
			defer reader.Close()
			defer writer.Close()
			cmd.Stderr = writer // Pass a real descriptor 2, not an exec copying writer.
			checkCapture(t, reader.SetReadDeadline(time.Now().Add(15*time.Second)))
			stdout, err := cmd.StdoutPipe()
			checkCapture(t, err)
			defer stdout.Close()
			stdin, err := cmd.StdinPipe()
			checkCapture(t, err)
			defer stdin.Close()
			checkCapture(t, cmd.Start())
			// Kill the child before closing its pipes if a handshake fails.
			defer func() {
				if cmd.ProcessState == nil {
					cmd.Process.Kill()
					cmd.Wait()
				}
			}()
			startup, err := bufio.NewReader(reader).ReadString('\n')
			want := fmt.Sprintf("conftamer: enabled — writing %q\n", capture)
			if err != nil || startup != want {
				t.Fatalf("startup = %q, error=%v; want %q", startup, err, want)
			}
			output := bufio.NewReader(stdout)
			if ready, err := output.ReadString('\n'); err != nil || ready != "ready\n" {
				t.Fatalf("child readiness = %q, error=%v", ready, err)
			}
			if full {
				fd := int(writer.Fd())
				checkCapture(t, syscall.SetNonblock(fd, true))
				for {
					if _, err := syscall.Write(fd, make([]byte, 4096)); err != nil {
						if err == syscall.EINTR {
							continue
						}
						if err != syscall.EAGAIN {
							t.Fatal(err)
						}
						break
					}
				}
				// Restore blocking mode before HTTP; nonblocking stderr would hide the bug.
				checkCapture(t, syscall.SetNonblock(fd, false))
			} else {
				checkCapture(t, reader.Close())
			}
			checkCapture(t, writer.Close())
			_, err = io.WriteString(stdin, "\n") // HTTP begins only after stderr is full/closed.
			checkCapture(t, err)
			checkCapture(t, stdin.Close())
			rest, err := io.ReadAll(output)
			checkCapture(t, err)
			if err := cmd.Wait(); err != nil {
				t.Fatalf("HTTP failed with full=%v stderr: %v\nstdout: %s", full, err, rest)
			}
			if !strings.Contains(string(rest), "HTTP completed with 204\n") {
				t.Fatalf("missing HTTP completion with full=%v stderr: %s", full, rest)
			}
		})
	}
}
