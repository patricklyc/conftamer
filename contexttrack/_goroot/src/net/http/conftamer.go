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
)

var (
	conftamerOnce    sync.Once
	conftamerMu      sync.Mutex
	conftamerFile    *os.File
	conftamerEnabled bool
	conftamerPath    string
	conftamerOpenErr error
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

// init prints a one-line diagnostic to stderr at process start whenever the
// user opted in via CONFTAMER_EVENTS.
// It runs eagerly (not lazily on the first hook) to make it easy to differentiate
// between "file exists but no events at all" and "file doesn't exist".
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

// --- event schema --------------------

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

// --- context ID (correlation key) -----------------------------------------
//
// An explicit, process-local, monotonically-increasing request ID stamped
// into the request context at its origin and inherited by every derived context.
// conftamerIDKey is the context.Value key for the stamped request ID.
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

// --- private outbound request copies ----------------------------------------
//
// An outbound request whose context lacks an ID is sent as a private copy with
// a stamped context: stamping the caller's request in place would race with
// its readers. Go exposes and cancels outbound requests by pointer
// (Response.Request, CheckRedirect's via, Transport.CancelRequest), so the copy
// records the caller's request and Go keeps using that one.

// conftamerOrigin links a private copy to the request it was made from. Copies
// of the copy (Go's own, or an application's) inherit it, but are not the copy.
type conftamerOrigin struct {
	copy, orig *Request
}

// conftamerStampRequest returns r if its context already carries an ID, else a
// private copy of r whose context carries a fresh one.
func conftamerStampRequest(r *Request) *Request {
	ctx := r.Context()
	if conftamerHasID(ctx) {
		return r
	}
	r2 := r.WithContext(conftamerStampID(ctx))
	r2.conftamer = &conftamerOrigin{copy: r2, orig: r}
	return r2
}

// conftamerOriginal returns the request r was copied from if r is a private
// copy made by conftamerStampRequest, else r.
func conftamerOriginal(r *Request) *Request {
	if r != nil && r.conftamer != nil && r.conftamer.copy == r {
		return r.conftamer.orig
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

// --- caller (API_ID) identification ----------------------

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
	Package  string
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

// Walks the current goroutine's stack and returns the first
// frame whose package is not in "conftamerHTTPLayerPackages"
// (inferred to be "generic helpers" and not the calling applciation).
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
			return &conftamerCaller{FuncName: f.Function, Package: pkg, ApiId: conftamerApiID(pkg)}
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
	// get "real" type if this is stored as "any"
	v := reflect.ValueOf(handler)
	var pkg, name string
	if v.Kind() == reflect.Func { // e.g., `http.HandlerFunc(...)`
		// Get function pointer -> look up address in `runtime` to get func metadata
		if fn := runtime.FuncForPC(v.Pointer()); fn != nil {
			name = fn.Name()
			pkg = conftamerPackageName(name)
		}
	} else { // e.g., method on a type
		t := v.Type()
		for t.Kind() == reflect.Ptr {
			t = t.Elem()
		}
		// Look for package where type is defined
		pkg = t.PkgPath()
		name = t.String()
	}
	if pkg == "" || conftamerHTTPLayerPackages[pkg] {
		return nil
	}
	return &conftamerCaller{FuncName: name, Package: pkg, ApiId: conftamerApiID(pkg)}
}

// --- metadata --------------------------------------------------------------

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

// --- logging entry point -----------------------------------------------

// conftamerLog records one HTTP event. ctx is the context with ID used for correlation.
// reqID (may be nil) is set for "Request sent". withCaller enables caller identification.
// handler (non-nil only for "Request received") is the not-yet-invoked Handler about to
// serve the request — when set, it's used instead of a stack walk, since the application
// handler isn't on the stack yet at that log point, and its name is recorded as Handler.
//
// "Response sent" deliberately does NOT identify a caller: at the point a response is
// written, the stack holds whatever the handler last called (fmt.Fprintln,
// gzip.(*Writer).Write), not the handler itself, so the walk yields a misleading API.
// Responses record only their code plus the method/path of the request they answer;
// analysis (contexttrack/analysis/message_graph.py) recovers api_id/handler by matching
// each response back to the "Request received" it answers, and the matched route pattern
// from the "Request routed" events logged by conftamerLogRouted.
func conftamerLog(kind string, msg map[string]string, ctx context.Context, reqID *conftamerRequestID, withCaller bool, handler any) {
	if !conftamerOn() {
		return
	}

	_, file, line, _ := runtime.Caller(1)

	ev := conftamerEvent{
		Kind:        kind,
		Pid:         os.Getpid(),
		GoroutineID: conftamerGoID(),
		File:        file,
		Line:        line,
		Message:     msg,
		Context:     conftamerContext(ctx),
	}

	if withCaller {
		var c *conftamerCaller
		if handler != nil {
			c = conftamerHandlerCaller(handler)
		} else {
			c = conftamerFindCaller()
		}
		if c != nil {
			ev.ApiId = c.ApiId
			if kind == "Request received" {
				ev.Handler = c.FuncName
			}
		}
	}
	if kind == "Request sent" {
		ev.RequestID = reqID
	}

	linebytes, err := json.Marshal(ev)
	if err != nil {
		return
	}
	conftamerMu.Lock()
	conftamerFile.Write(append(linebytes, '\n'))
	conftamerMu.Unlock()
}

// conftamerWillReject reports whether Transport.roundTrip will reject req without
// trying to send it, so that it is not logged as sent. The checks are mirrored
// only for requests that no alternate protocol can take first: a cached HTTP/2
// connection, for example, sends methods that the HTTP/1 path rejects.
func (t *Transport) conftamerWillReject(req *Request, isHTTP bool) bool {
	return t.alternateRoundTripper(req) == nil &&
		(!isHTTP || req.Method != "" && !validMethod(req.Method) || req.URL.Host == "")
}

// conftamerLogRouted records the route pattern a ServeMux matched for r, logged from
// ServeMux.ServeHTTP because that's the only point the pattern is reliably in hand:
// "Request received" fires before any routing happens, and by "Response sent" a nested
// mux's pattern has been written to a shallow request copy (see StripPrefix) that the
// ResponseWriter's request never sees. Nested muxes each log, sharing the request's
// context ID, so the last one recorded is the innermost — the most specific match.
// An empty pattern (no route matched) is not logged.
func conftamerLogRouted(pattern string, r *Request) {
	if pattern == "" {
		return
	}
	conftamerLog("Request routed", map[string]string{
		"pattern":      pattern,
		"req.Method":   r.Method,
		"req.URL.Path": r.URL.Path,
	}, r.Context(), nil, false, nil)
}

// ConftamerLogRouted records a route pattern matched by a router outside this
// package — see conftamerLogRouted. Exported because a router such as
// prometheus/common/route does its own matching, and can reach neither the
// unexported logger nor the context ID it correlates on.
func ConftamerLogRouted(pattern string, r *Request) {
	conftamerLogRouted(pattern, r)
}

// Response received at HTTP/1 header parsing or HTTP/2 header delivery, including
// direct Transport.RoundTrip calls. No caller ID: the HTTP/1 read loop has no
// application caller on its stack; consumers can attribute either protocol's
// response to its request. r may be nil when request metadata is unavailable.
func conftamerLogResponseWire(kind string, code int, ctx context.Context, r *Request) {
	if !conftamerOn() {
		return
	}
	codeKey := "code"
	if kind == "Response received" {
		codeKey = "resp.StatusCode"
	}
	msg := map[string]string{codeKey: strconv.Itoa(code)}
	if r != nil {
		msg["req.Method"] = valueOrDefault(r.Method, MethodGet)
		if r.URL != nil {
			msg["req.URL.Path"] = r.URL.Path
		}
		if ctx == nil {
			ctx = r.Context()
		}
	}
	conftamerLog(kind, msg, ctx, nil, false, nil)
}
