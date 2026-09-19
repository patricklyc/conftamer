package http

import (
	"context"
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
	return &conftamerExchange{
		id:        conftamerExchangeCounter.Add(1),
		contextID: contextID,
		origin: conftamerOrigin{
			request: conftamerRequestSnapshot(req),
			server:  server,
		},
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
	event := conftamerRequestEvent{
		ContextID: contextID,
		Request:   exchange.origin.request,
		APIID:     nil,
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
