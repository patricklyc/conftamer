package http

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
)

var (
	conftamerContextCounter  atomic.Uint64
	conftamerExchangeCounter atomic.Uint64
)

type conftamerContextKey struct{}

// ConftamerContext returns a context carrying a new capture-local root. It
// returns ctx unchanged when capture is disabled or the context already has a
// root. Callers must use the returned context. A nil context remains nil.
func ConftamerContext(ctx context.Context) context.Context {
	if ctx == nil || !conftamerCaptureEnabled() {
		return ctx
	}
	if _, ok := conftamerContextID(ctx); ok {
		return ctx
	}
	return context.WithValue(ctx, conftamerContextKey{}, conftamerContextCounter.Add(1))
}

// ConftamerWithClientAPI returns a shallow copy of r with an explicit API
// owner bound to its current method, authority, and path. The binding is used
// only if those target fields are unchanged when a transport attempt begins.
func ConftamerWithClientAPI(r *Request, apiID string) *Request {
	if r == nil || !conftamerCaptureEnabled() {
		return r
	}
	if !conftamerValidMetadataText("api_id", apiID) {
		return r
	}
	copy := new(Request)
	*copy = *r
	copy.conftamerClientAPI = &conftamerClientAPIBinding{
		apiID:   apiID,
		request: conftamerRequestSnapshot(r),
	}
	return copy
}

// ConftamerSetServerAPI records an explicit API owner for a traced server
// request. Repeated calls are retained as separate metadata observations.
func ConftamerSetServerAPI(r *Request, apiID string) {
	exchange := conftamerServerExchange(r)
	if exchange == nil || !conftamerValidMetadataText("api_id", apiID) {
		return
	}
	event := conftamerMetadataEvent{APIID: conftamerStringPointer(apiID)}
	event.ExchangeID = exchange.id
	event.Kind = "request_metadata"
	conftamerWriteRecord(&event.conftamerEnvelope, &event)
}

// ConftamerLogRouted records a route matched for a traced server request.
// full_pattern is present only when native StripPrefix state proves how the
// local pattern maps into the original request's path space.
func ConftamerLogRouted(r *Request, dialect, pattern string) {
	exchange := conftamerServerExchange(r)
	if exchange == nil || !conftamerValidDialect(dialect) || !conftamerValidMetadataText("route.pattern", pattern) {
		return
	}
	current := conftamerRequestSnapshot(r)
	var fullPattern *string
	if conftamerSameMethodAndAuthority(exchange.origin.request, current) {
		if full, ok := conftamerFullPattern(exchange.origin.request.Path, r.conftamerStrippedPrefix, current.Path, pattern); ok {
			fullPattern = conftamerStringPointer(full)
		}
	}
	event := conftamerMetadataEvent{Route: &conftamerRoute{
		Dialect:     dialect,
		Pattern:     pattern,
		MatchedPath: current.Path,
		FullPattern: fullPattern,
	}}
	event.ExchangeID = exchange.id
	event.Kind = "request_metadata"
	conftamerWriteRecord(&event.conftamerEnvelope, &event)
}

func conftamerCaptureEnabled() bool {
	log := conftamerActiveLogger.Load()
	return log != nil && !log.stopped.Load()
}

func conftamerContextID(ctx context.Context) (uint64, bool) {
	if ctx == nil {
		return 0, false
	}
	id, ok := ctx.Value(conftamerContextKey{}).(uint64)
	return id, ok && id != 0
}

func conftamerAttachServerRequest(req *Request) {
	if req.conftamerExchange != nil || !conftamerCaptureEnabled() {
		return
	}
	contextID := conftamerContextCounter.Add(1)
	req.ctx = context.WithValue(req.Context(), conftamerContextKey{}, contextID)
	exchange := conftamerNewExchange(req, true, contextID)
	req.conftamerExchange = exchange
	conftamerLogRequest(exchange, "receive_request")
}

func conftamerNewClientExchange(req *Request) *conftamerExchange {
	if !conftamerCaptureEnabled() {
		return nil
	}
	contextID, _ := conftamerContextID(req.Context())
	return conftamerNewExchange(req, false, contextID)
}

func conftamerNewExchange(req *Request, server bool, contextID uint64) *conftamerExchange {
	request := conftamerRequestSnapshot(req)
	origin := conftamerOrigin{request: request, server: server}
	if !server && req.conftamerClientAPI != nil && conftamerSameRequestLabel(req.conftamerClientAPI.request, request) {
		origin.apiID = req.conftamerClientAPI.apiID
	}
	return &conftamerExchange{
		id:        conftamerExchangeCounter.Add(1),
		contextID: contextID,
		origin:    origin,
	}
}

func conftamerRequestSnapshot(req *Request) conftamerRequestLabel {
	method := req.Method
	if method == "" {
		method = "GET"
	}
	host := req.Host
	path := ""
	if req.URL != nil {
		if host == "" {
			host = req.URL.Host
		}
		path = req.URL.Path
	}
	var hostPointer *string
	if host != "" {
		hostPointer = &host
	}
	return conftamerRequestLabel{Method: method, Host: hostPointer, Path: path}
}

func conftamerLogRequest(exchange *conftamerExchange, kind string) {
	if exchange == nil {
		return
	}
	var contextID *uint64
	if exchange.contextID != 0 {
		value := exchange.contextID
		contextID = &value
	}
	var apiID *string
	if exchange.origin.apiID != "" {
		apiID = conftamerStringPointer(exchange.origin.apiID)
	}
	event := conftamerRequestEvent{
		ContextID: contextID,
		Request:   exchange.origin.request,
		APIID:     apiID,
	}
	event.ExchangeID = exchange.id
	event.Kind = kind
	conftamerWriteRecord(&event.conftamerEnvelope, &event)
}

func conftamerLogResponse(exchange *conftamerExchange, kind string, statusCode int) {
	if exchange == nil {
		return
	}
	event := conftamerResponseEvent{StatusCode: statusCode}
	event.ExchangeID = exchange.id
	event.Kind = kind
	conftamerWriteRecord(&event.conftamerEnvelope, &event)
}

func conftamerServerExchange(r *Request) *conftamerExchange {
	if r == nil || !conftamerCaptureEnabled() || r.conftamerExchange == nil || !r.conftamerExchange.origin.server {
		return nil
	}
	return r.conftamerExchange
}

func conftamerValidMetadataText(field, value string) bool {
	if value == "" {
		conftamerFailCapture(fmt.Errorf("conftamer %s must be nonempty", field))
		return false
	}
	if err := conftamerValidateString(field, value); err != nil {
		conftamerFailCapture(err)
		return false
	}
	return true
}

func conftamerValidDialect(dialect string) bool {
	switch dialect {
	case "go_serve_mux", "go_serve_mux_121", "httprouter":
		return true
	default:
		conftamerFailCapture(fmt.Errorf("conftamer route dialect %q is unsupported", dialect))
		return false
	}
}

func conftamerFullPattern(originalPath, prefix, matchedPath, pattern string) (string, bool) {
	if originalPath != prefix+matchedPath {
		return "", false
	}
	slash := strings.IndexByte(pattern, '/')
	if slash < 0 {
		return "", false
	}
	return pattern[:slash] + prefix + pattern[slash:], true
}

func conftamerSameMethodAndAuthority(left, right conftamerRequestLabel) bool {
	return left.Method == right.Method && conftamerOptionalStringEqual(left.Host, right.Host)
}

func conftamerSameRequestLabel(left, right conftamerRequestLabel) bool {
	return conftamerSameMethodAndAuthority(left, right) && left.Path == right.Path
}

func conftamerOptionalStringEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func conftamerStringPointer(value string) *string { return &value }

// conftamerEnvelope identifies one record within a capture process.
type conftamerEnvelope struct {
	SchemaVersion int    `json:"schema_version"`
	CaptureID     string `json:"capture_id"`
	ProcessID     string `json:"process_id"`
	Seq           uint64 `json:"seq"`
	ExchangeID    uint64 `json:"exchange_id"`
	Kind          string `json:"kind"`
}

type conftamerRequestLabel struct {
	Method string  `json:"method"`
	Host   *string `json:"host"`
	Path   string  `json:"path"`
}

type conftamerRoute struct {
	Dialect     string  `json:"dialect"`
	Pattern     string  `json:"pattern"`
	MatchedPath string  `json:"matched_path"`
	FullPattern *string `json:"full_pattern"`
}

type conftamerRequestEvent struct {
	conftamerEnvelope
	ContextID *uint64               `json:"context_id"`
	Request   conftamerRequestLabel `json:"request"`
	APIID     *string               `json:"api_id"`
}

type conftamerResponseEvent struct {
	conftamerEnvelope
	StatusCode int `json:"status_code"`
}

type conftamerMetadataEvent struct {
	conftamerEnvelope
	Route *conftamerRoute `json:"route"`
	APIID *string         `json:"api_id"`
}

// conftamerExchange is immutable after publication to HTTP protocol workers.
// A zero contextID means that the request had no stamped context root.
type conftamerExchange struct {
	id        uint64
	contextID uint64
	origin    conftamerOrigin
}

// conftamerOrigin is the request label snapshot owned by an exchange.
type conftamerOrigin struct {
	request conftamerRequestLabel
	apiID   string
	server  bool
}

// conftamerClientAPIBinding is immutable after its request copy is returned.
type conftamerClientAPIBinding struct {
	apiID   string
	request conftamerRequestLabel
}
