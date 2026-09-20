package http

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

var (
	errConftamerInvalidUTF8         = errors.New("conftamer record contains invalid UTF-8")
	errConftamerUnsupportedProtocol = errors.New("non-HTTP/1 capture is unsupported")
	conftamerActiveLogger           atomic.Pointer[conftamerLogger]
)

type conftamerLogger struct {
	writer      io.Writer
	diagnostics io.Writer
	captureID   string
	processID   string

	mu       sync.Mutex
	seq      uint64
	firstErr error
	stopped  atomic.Bool
}

func newConftamerLogger(w io.Writer, captureID, processID string) *conftamerLogger {
	return &conftamerLogger{
		writer:      w,
		diagnostics: os.Stderr,
		captureID:   captureID,
		processID:   processID,
	}
}

// write assigns the process-local sequence and writes one complete JSON line.
// The first encoding or I/O failure permanently stops this logger.
func (log *conftamerLogger) write(header *conftamerEnvelope, record any) error {
	log.mu.Lock()
	defer log.mu.Unlock()

	if log.firstErr != nil {
		return log.firstErr
	}
	if header == nil {
		return log.failLocked(errors.New("conftamer record has a nil envelope"))
	}
	if log.seq == ^uint64(0) {
		return log.failLocked(errors.New("conftamer sequence exhausted"))
	}

	header.SchemaVersion = 3
	header.CaptureID = log.captureID
	header.ProcessID = log.processID
	header.Seq = log.seq + 1
	if err := conftamerValidateStrings(header, record); err != nil {
		return log.failLocked(err)
	}
	line, err := json.Marshal(record)
	if err != nil {
		return log.failLocked(fmt.Errorf("marshal conftamer record: %w", err))
	}
	line = append(line, '\n')
	written, err := log.writer.Write(line)
	if err != nil {
		return log.failLocked(fmt.Errorf("write conftamer record: %w", err))
	}
	if written != len(line) {
		return log.failLocked(io.ErrShortWrite)
	}
	log.seq = header.Seq
	return nil
}

func (log *conftamerLogger) failLocked(err error) error {
	if log.firstErr == nil {
		log.firstErr = err
		log.stopped.Store(true)
		if log.diagnostics != nil {
			fmt.Fprintf(log.diagnostics, "conftamer: capture failed: %v\n", err)
		}
	}
	return log.firstErr
}

// conftamerWriteRecord provides the lock-free disabled/stopped fast path used
// by protocol hooks. Capture errors are diagnostics and never alter HTTP results.
func conftamerWriteRecord(header *conftamerEnvelope, record any) {
	log := conftamerActiveLogger.Load()
	if log == nil || log.stopped.Load() {
		return
	}
	_ = log.write(header, record)
}

func conftamerFailCapture(err error) {
	log := conftamerActiveLogger.Load()
	if log == nil || log.stopped.Load() {
		return
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	log.failLocked(err)
}

func conftamerValidateStrings(header *conftamerEnvelope, record any) error {
	if err := conftamerValidateString("capture_id", header.CaptureID); err != nil {
		return err
	}
	if err := conftamerValidateString("process_id", header.ProcessID); err != nil {
		return err
	}
	if err := conftamerValidateString("kind", header.Kind); err != nil {
		return err
	}

	switch event := record.(type) {
	case *conftamerRequestEvent:
		if err := conftamerValidateString("request.method", event.Request.Method); err != nil {
			return err
		}
		if err := conftamerValidateOptionalString("request.host", event.Request.Host); err != nil {
			return err
		}
		return conftamerValidateString("request.path", event.Request.Path)
	}
	return nil
}

func conftamerValidateString(field, value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w in %s", errConftamerInvalidUTF8, field)
	}
	return nil
}

func conftamerValidateOptionalString(field string, value *string) error {
	if value == nil {
		return nil
	}
	return conftamerValidateString(field, *value)
}

func init() {
	log, path, err := conftamerLoggerFromEnvironment()
	if err != nil {
		fmt.Fprintf(os.Stderr, "conftamer: configuration error: %v\n", err)
		return
	}
	if log == nil {
		return
	}
	conftamerActiveLogger.Store(log)
	fmt.Fprintf(os.Stderr, "conftamer: enabled capture_id=%q process_id=%q path=%q\n", log.captureID, log.processID, path)
}

func conftamerLoggerFromEnvironment() (*conftamerLogger, string, error) {
	if _, legacySet := os.LookupEnv("CONFTAMER_EVENTS"); legacySet {
		return nil, "", errors.New("CONFTAMER_EVENTS is unsupported; use CONFTAMER_EVENTS_DIR and CONFTAMER_CAPTURE_ID")
	}

	directory, directorySet := os.LookupEnv("CONFTAMER_EVENTS_DIR")
	captureID, captureIDSet := os.LookupEnv("CONFTAMER_CAPTURE_ID")
	if !directorySet && !captureIDSet {
		return nil, "", nil
	}
	if !directorySet || !captureIDSet {
		return nil, "", errors.New("CONFTAMER_EVENTS_DIR and CONFTAMER_CAPTURE_ID must both be set")
	}
	if captureID == "" {
		return nil, "", errors.New("CONFTAMER_CAPTURE_ID must be nonempty")
	}
	if !utf8.ValidString(captureID) {
		return nil, "", errors.New("CONFTAMER_CAPTURE_ID must be valid UTF-8")
	}
	if !utf8.ValidString(directory) {
		return nil, "", errors.New("CONFTAMER_EVENTS_DIR must be valid UTF-8")
	}
	if !filepath.IsAbs(directory) {
		return nil, "", errors.New("CONFTAMER_EVENTS_DIR must be absolute")
	}
	info, err := os.Stat(directory)
	if err != nil {
		return nil, "", fmt.Errorf("inspect CONFTAMER_EVENTS_DIR: %w", err)
	}
	if !info.IsDir() {
		return nil, "", errors.New("CONFTAMER_EVENTS_DIR must name a directory")
	}

	file, processID, path, err := conftamerCreateProcessFile(directory)
	if err != nil {
		return nil, "", err
	}
	return newConftamerLogger(file, captureID, processID), path, nil
}

func conftamerCreateProcessFile(directory string) (*os.File, string, string, error) {
	for range 10 {
		var identity [16]byte
		if _, err := rand.Read(identity[:]); err != nil {
			return nil, "", "", fmt.Errorf("generate process identity: %w", err)
		}
		processID := hex.EncodeToString(identity[:])
		path := filepath.Join(directory, processID+".jsonl")
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return file, processID, path, nil
		}
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return nil, "", "", fmt.Errorf("create process capture file: %w", err)
	}
	return nil, "", "", errors.New("could not allocate a unique process capture file")
}
