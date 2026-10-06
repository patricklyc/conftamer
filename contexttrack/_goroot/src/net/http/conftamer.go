// ConfTamer instrumentation embedded in net/http.
//
// At a handful of HTTP funnel points, records the message, resolves
// the request context's conftamer ID, and tries to identify the "API ID"
// (application-level caller).
// A JSON object is appended to the file named by $CONFTAMER_EVENTS per event.
// If $CONFTAMER_EVENTS is unset, every hook is a no-op.

package http

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
)

var (
	conftamerOnce          sync.Once
	conftamerWriteWarnOnce sync.Once
	conftamerMu            sync.Mutex
	conftamerFile          *os.File
	conftamerEnabled       bool
	conftamerPath          string
	conftamerOpenErr       error
)

func conftamerInit() {
	conftamerPath = os.Getenv("CONFTAMER_EVENTS")
	if conftamerPath == "" {
		return
	}
	f, err := os.OpenFile(conftamerPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		conftamerOpenErr = err
		return
	}
	conftamerFile = f
	conftamerEnabled = true
}

func conftamerOn() bool {
	conftamerOnce.Do(conftamerInit)
	return conftamerEnabled
}

// init reports opt-in capture status at process start, distinguishing an empty
// capture from an open failure before any hook runs.
func init() {
	if os.Getenv("CONFTAMER_EVENTS") == "" {
		return
	}
	if conftamerOn() {
		fmt.Fprintf(os.Stderr, "conftamer: enabled — writing %q\n", conftamerPath)
	} else {
		fmt.Fprintf(os.Stderr, "conftamer: CONFTAMER_EVENTS=%q set but open failed: %v\n", conftamerPath, conftamerOpenErr)
	}
}

// Event schema.

type conftamerEvent struct {
	Kind        string                `json:"kind"`
	Pid         int                   `json:"pid"`
	GoroutineID int                   `json:"goroutine_id"`
	ThreadID    int                   `json:"thread_id"`
	File        string                `json:"file"`
	Line        int                   `json:"line"`
	Message     map[string]string     `json:"message"`
	Context     *conftamerContextInfo `json:"context"`
	RequestID   *conftamerRequestID   `json:"request_id,omitempty"`
	ApiId       string                `json:"api_id,omitempty"`
	// Handler is the name of the Handler serving a "Request received" event.
	// Not set on "Response sent".
	Handler string `json:"handler,omitempty"`
}

type conftamerRequestID struct {
	Method string `json:"method"`
	Host   string `json:"host"`
	Path   string `json:"path"`
}

type conftamerContextInfo struct {
	Source    string `json:"source,omitempty"`
	Type      string `json:"type,omitempty"`
	ContextID string `json:"context_id,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Context ID (correlation key).
//
// An explicit, process-local, monotonically-increasing context ID stamped
// into the request context at its origin and inherited by every derived context.
// conftamerIDKey is the context.Value key for the stamped context ID.
// It's unexported to avoid colliding with other context.Value keys.
type conftamerIDKeyType struct{}

var (
	conftamerIDKey  conftamerIDKeyType
	conftamerNextID atomic.Uint64 // Next to assign
)

func conftamerHasID(ctx context.Context) bool {
	_, ok := ctx.Value(conftamerIDKey).(uint64)
	return ok
}

// conftamerStampID returns ctx unchanged if it already carries an ID
// (inherited from an ancestor), else a child context carrying a fresh
// monotonic ID.
func conftamerStampID(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if conftamerHasID(ctx) {
		return ctx
	}
	return context.WithValue(ctx, conftamerIDKey, conftamerNextID.Add(1))
}

// Requests without IDs use private stamped copies to avoid caller-reader races.
// Keep the original pointer for Response.Request, redirects, and cancellation;
// Go's or an application's copies of the stamped copy retain their own identity.
type conftamerOrigin struct {
	stampedCopy, originalRequest *Request
}

// conftamerStampRequest returns r if its context already carries an ID, else a
// private copy of r whose context carries a fresh one.
func conftamerStampRequest(r *Request) *Request {
	ctx := r.Context()
	if conftamerHasID(ctx) {
		return r
	}
	stampedCopy := r.WithContext(conftamerStampID(ctx))
	stampedCopy.conftamer = &conftamerOrigin{stampedCopy: stampedCopy, originalRequest: r}
	return stampedCopy
}

// conftamerOriginal returns the request r was copied from if r is a private
// copy made by conftamerStampRequest, else r.
func conftamerOriginal(r *Request) *Request {
	if r != nil && r.conftamer != nil && r.conftamer.stampedCopy == r {
		return r.conftamer.originalRequest
	}
	return r
}

// conftamerRestoreRequest makes resp expose the caller's request when a
// RoundTripper returned a private copy in its place.
func conftamerRestoreRequest(resp *Response) {
	if !conftamerOn() || resp == nil {
		return
	}
	if orig := conftamerOriginal(resp.Request); orig != resp.Request {
		resp.Request = orig
	}
}

// conftamerStampIDIfOn is conftamerStampID gated on the logger being enabled.
func conftamerStampIDIfOn(ctx context.Context) context.Context {
	if !conftamerOn() {
		return ctx
	}
	return conftamerStampID(ctx)
}

func conftamerContext(ctx context.Context) *conftamerContextInfo {
	if ctx == nil {
		return &conftamerContextInfo{Error: "nil context"}
	}
	id, ok := ctx.Value(conftamerIDKey).(uint64)
	if !ok {
		// IDs belong on the request context, not on individual log records.
		return &conftamerContextInfo{Error: "missing context ID"}
	}
	return &conftamerContextInfo{
		Source:    "req.Context()",
		Type:      "context.Context",
		ContextID: "id:" + strconv.FormatUint(id, 10),
	}
}

// Caller (API ID) identification. Stack-derived attribution is best-effort
// debug metadata, not a stable runtime interface or module ownership.

var conftamerHTTPLayerPackages = map[string]bool{
	"net/http":               true,
	"golang.org/x/net/http2": true,
}

var conftamerMultiTenantHosts = map[string]bool{
	"github.com":    true,
	"gitlab.com":    true,
	"bitbucket.org": true,
}

type conftamerCaller struct {
	FuncName string
	ApiId    string
}

// tries to extract the import path from a runtime fully-qualified
// function name, e.g. "github.com/prometheus/prometheus/scrape.(*x).scrape" ->
// "github.com/prometheus/prometheus/scrape".
// The boundary is: drop any "[...]" generic suffix, find the last "/", then the
// first "." after it.
func conftamerPackageName(fullName string) string {
	name := fullName
	if start := strings.Index(name, "["); start >= 0 {
		name = name[:start]
	}
	pathEnd := strings.LastIndex(name, "/")
	if pathEnd < 0 {
		pathEnd = 0
	}
	if dot := strings.Index(name[pathEnd:], "."); dot >= 0 {
		return name[:pathEnd+dot]
	}
	return ""
}

// Derives an API id from a package import path, e.g.
// "github.com/prometheus/prometheus/scrape" -> "github.com/prometheus",
// "k8s.io/client-go/rest" -> "k8s.io". Stdlib paths (no dot in the first
// segment) returned unchanged.
func conftamerApiID(pkg string) string {
	if pkg == "" {
		return ""
	}
	segs := strings.SplitN(pkg, "/", 3)
	if !strings.Contains(segs[0], ".") {
		return pkg
	}
	if len(segs) >= 2 && conftamerMultiTenantHosts[segs[0]] {
		return segs[0] + "/" + segs[1]
	}
	return segs[0]
}

// conftamerFindCaller infers the application caller from the first stack frame
// outside conftamerHTTPLayerPackages.
func conftamerFindCaller() *conftamerCaller {
	var pcs [50]uintptr
	n := runtime.Callers(2, pcs[:])
	if n == 0 {
		return nil
	}
	frames := runtime.CallersFrames(pcs[:n])
	for {
		f, more := frames.Next()
		pkg := conftamerPackageName(f.Function)
		if pkg != "" && !conftamerHTTPLayerPackages[pkg] {
			return &conftamerCaller{FuncName: f.Function, ApiId: conftamerApiID(pkg)}
		}
		if !more {
			break
		}
	}
	return nil
}

// conftamerHandlerCaller identifies the API ID from a not-yet-invoked Handler
// value. Used for "Request received", where the stack doesn't include the
// application handler.
func conftamerHandlerCaller(handler any) *conftamerCaller {
	if handler == nil {
		return nil
	}
	v := reflect.ValueOf(handler)
	var pkg, name string
	if v.Kind() == reflect.Func { // e.g., `http.HandlerFunc(...)`
		if fn := runtime.FuncForPC(v.Pointer()); fn != nil {
			name = fn.Name()
			pkg = conftamerPackageName(name)
		}
	} else { // e.g., method on a type
		t := v.Type()
		for t.Kind() == reflect.Ptr {
			t = t.Elem()
		}
		pkg = t.PkgPath()
		name = t.String()
	}
	if pkg == "" || conftamerHTTPLayerPackages[pkg] {
		return nil
	}
	return &conftamerCaller{FuncName: name, ApiId: conftamerApiID(pkg)}
}

// conftamerGoID parses runtime.Stack's debug text; this is not a stable runtime
// goroutine-identity API and must be rechecked on Go upgrades.
func conftamerGoID() int {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	s := string(buf[:n])
	s = strings.TrimPrefix(s, "goroutine ")
	if i := strings.IndexByte(s, ' '); i >= 0 {
		id, _ := strconv.Atoi(s[:i])
		return id
	}
	return 0
}

// Helpers build payloads; the logger fills the envelope and writes JSONL.
// Preserve provenance: direct hooks name their stock Go file; route/wire hooks
// name conftamer.go. With overlays, line numbers refer to the patched source.
const (
	conftamerHookSource   = 2 // caller of the event-specific helper
	conftamerHelperSource = 1 // the event-specific helper itself
)

func conftamerLog(ev conftamerEvent, ctx context.Context, sourceDepth int) {
	if !conftamerOn() {
		return
	}
	_, ev.File, ev.Line, _ = runtime.Caller(sourceDepth)
	ev.Pid = os.Getpid()
	ev.GoroutineID = conftamerGoID()
	ev.Context = conftamerContext(ctx)

	lineBytes, err := json.Marshal(ev)
	if err != nil {
		return
	}
	conftamerMu.Lock()
	_, err = conftamerFile.Write(append(lineBytes, '\n'))
	conftamerMu.Unlock()
	if err != nil {
		conftamerWriteWarnOnce.Do(func() {
			go conftamerWriteWarning(os.Stderr, syscall.Write, err)
		})
	}
}

// The single warning worker may block, but HTTP does not wait for it. Protect
// the captured stderr File without os.File.Write's SIGPIPE exit; FD is portable.
func conftamerWriteWarning[FD ~int | ~uintptr](stderr *os.File, write func(FD, []byte) (int, error), captureErr error) {
	conn, err := stderr.SyscallConn()
	if err != nil {
		return
	}
	warning := []byte(fmt.Sprintf("conftamer: capture write failed for %q: %v; capture may be incomplete or invalid\n", conftamerPath, captureErr))
	_ = conn.Write(func(fd uintptr) bool {
		_, _ = write(FD(fd), warning)
		return true // One best-effort attempt, even on error or a short write.
	})
}

func conftamerRequestEvent(kind string, req *Request) conftamerEvent {
	return conftamerEvent{Kind: kind, Message: map[string]string{
		"req.Method": req.Method, "req.URL.Path": req.URL.Path,
	}}
}

func conftamerLogRequestSent(req *Request) {
	if !conftamerOn() {
		return
	}
	ev := conftamerRequestEvent("Request sent", req)
	method := valueOrDefault(req.Method, MethodGet)
	ev.Message["req.Method"] = method
	ev.Message["req.URL.Host"] = req.URL.Host
	ev.Message["req.URL.RawQuery"] = req.URL.RawQuery
	ev.RequestID = &conftamerRequestID{Method: method, Host: req.URL.Host, Path: req.URL.Path}
	if caller := conftamerFindCaller(); caller != nil {
		ev.ApiId = caller.ApiId
	}
	conftamerLog(ev, req.Context(), conftamerHookSource)
}

// The handler has not run yet, so identify it from its value, not the stack.
func conftamerLogRequestReceived(req *Request, handler Handler) {
	if !conftamerOn() {
		return
	}
	ev := conftamerRequestEvent("Request received", req)
	ev.Message["req.URL.RawQuery"] = req.URL.RawQuery
	caller := conftamerHandlerCaller(handler)
	if handler == nil {
		caller = conftamerFindCaller()
	}
	if caller != nil {
		ev.ApiId = caller.ApiId
		ev.Handler = caller.FuncName
	}
	conftamerLog(ev, req.Context(), conftamerHookSource)
}

// Do not identify a caller here: fmt/gzip/etc. can obscure the handler on the
// stack. Consumers may associate this response with its received request.
func conftamerLogResponseSent(req *Request, code int) {
	if !conftamerOn() {
		return
	}
	ev := conftamerRequestEvent("Response sent", req)
	ev.Message["code"] = strconv.Itoa(code)
	conftamerLog(ev, req.Context(), conftamerHookSource)
}

// Client.do reports the caller's hop labels and the stamped send context.
func conftamerLogResponseReceived(req *Request, resp *Response, ctx context.Context) {
	if !conftamerOn() {
		return
	}
	ev := conftamerRequestEvent("Response received", req)
	ev.Message["req.Method"] = valueOrDefault(req.Method, MethodGet)
	ev.Message["resp.StatusCode"] = strconv.Itoa(resp.StatusCode)
	if caller := conftamerFindCaller(); caller != nil {
		ev.ApiId = caller.ApiId
	}
	conftamerLog(ev, ctx, conftamerHookSource)
}

// conftamerWillReject reports whether Transport.roundTrip will reject req without
// trying to send it, so that it is not logged as sent. Keep it in step with that
// function's validation/alternate-dispatch ordering on Go upgrades: the hook runs
// after header/trailer validation, before alternate dispatch and HTTP/1 checks.
// Mirror those checks only when no alternate protocol can take the request first:
// a cached HTTP/2 connection can try methods that the HTTP/1 path rejects.
func (t *Transport) conftamerWillReject(req *Request, isHTTP bool) bool {
	return t.alternateRoundTripper(req) == nil &&
		(!isHTTP || req.Method != "" && !validMethod(req.Method) || req.URL.Host == "")
}

// Record patterns during routing: receipt is too early, and nested muxes may
// later put patterns on copies the ResponseWriter never sees (e.g. StripPrefix).
// Multiple observations can share a context; consumers decide associations.
func conftamerLogRouted(pattern string, r *Request) {
	if pattern == "" || !conftamerOn() {
		return
	}
	ev := conftamerRequestEvent("Request routed", r)
	ev.Message["pattern"] = pattern
	conftamerLog(ev, r.Context(), conftamerHelperSource)
}

// ConftamerLogRouted lets external routers report matches with the same context
// correlation as ServeMux, without access to the private logger or context key.
func ConftamerLogRouted(pattern string, r *Request) {
	conftamerLogRouted(pattern, r)
}

// Response received at HTTP/1 header parsing or HTTP/2 header delivery, including
// direct Transport.RoundTrip calls; Client.do may log the same response again.
// No caller ID: the HTTP/1 read loop has no application caller on its stack.
// Consumers can attribute either protocol's response to its request.
// r may be nil when request metadata is unavailable.
func conftamerLogResponseWire(code int, ctx context.Context, r *Request) {
	if !conftamerOn() {
		return
	}
	msg := map[string]string{"resp.StatusCode": strconv.Itoa(code)}
	if r != nil {
		msg["req.Method"] = valueOrDefault(r.Method, MethodGet)
		if r.URL != nil {
			msg["req.URL.Path"] = r.URL.Path
		}
		if ctx == nil {
			ctx = r.Context()
		}
	}
	conftamerLog(conftamerEvent{Kind: "Response received", Message: msg}, ctx, conftamerHelperSource)
}
