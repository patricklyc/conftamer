package http

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
)

var (
	conftamerExchangeCounter         atomic.Uint64
	errConftamerInvalidRequest       = errors.New("conftamer request annotation target is nil")
	errConftamerInvalidSource        = errors.New("conftamer source was not observed as a receive")
	errConftamerInvalidReplyTarget   = errors.New("conftamer reply target was not observed as a server request")
	errConftamerLateReplyDeclaration = errors.New("reply sources declared after final headers")
)

// ConftamerSource is an HTTP request or response observed as a receive by an
// enabled ConfTamer capture. Only *Request and *Response implement this
// interface.
type ConftamerSource interface {
	conftamerReceiveSeq() uint64
}

func (req *Request) conftamerReceiveSeq() uint64 {
	if req == nil {
		return 0
	}
	return req.conftamerReceiveSequence
}

func (resp *Response) conftamerReceiveSeq() uint64 {
	if resp == nil {
		return 0
	}
	return resp.conftamerReceiveSequence
}

// ConftamerWithSources returns a shallow copy of req whose complete declared
// source list is sources. It returns req unchanged when capture is disabled or
// stopped. Invalid declarations fail capture without changing req.
func ConftamerWithSources(req *Request, sources ...ConftamerSource) *Request {
	if !conftamerCaptureEnabled() {
		return req
	}
	if req == nil {
		conftamerFailCapture(errConftamerInvalidRequest)
		return req
	}
	sequences, ok := conftamerSourceSequences(sources)
	if !ok {
		conftamerFailCapture(errConftamerInvalidSource)
		return req
	}
	annotated := new(Request)
	*annotated = *req
	annotated.conftamerSources = sequences
	return annotated
}

// ConftamerSetReplySources replaces the additional declared sources for the
// reply associated with req. The received req is always an automatic source.
// Declarations after final headers fail capture without changing the reply.
func ConftamerSetReplySources(req *Request, sources ...ConftamerSource) {
	if !conftamerCaptureEnabled() {
		return
	}
	if req == nil || req.conftamerExchange == nil || req.conftamerReceiveSequence == 0 || req.conftamerReply == nil {
		conftamerFailCapture(errConftamerInvalidReplyTarget)
		return
	}
	sequences, ok := conftamerSourceSequences(sources)
	if !ok {
		conftamerFailCapture(errConftamerInvalidSource)
		return
	}
	if !req.conftamerReply.set(sequences) {
		conftamerFailCapture(errConftamerLateReplyDeclaration)
	}
}

func conftamerCaptureEnabled() bool {
	log := conftamerActiveLogger.Load()
	return log != nil && !log.stopped.Load()
}

func conftamerSourceSequences(sources []ConftamerSource) ([]uint64, bool) {
	sequences := make([]uint64, 0, len(sources))
	for _, source := range sources {
		if source == nil {
			return nil, false
		}
		sequence := source.conftamerReceiveSeq()
		if sequence == 0 {
			return nil, false
		}
		sequences = append(sequences, sequence)
	}
	return conftamerCanonicalSources(sequences), true
}

func conftamerCanonicalSources(sources []uint64) []uint64 {
	canonical := append([]uint64(nil), sources...)
	sort.Slice(canonical, func(left, right int) bool { return canonical[left] < canonical[right] })
	unique := canonical[:0]
	for _, source := range canonical {
		if len(unique) == 0 || unique[len(unique)-1] != source {
			unique = append(unique, source)
		}
	}
	if unique == nil {
		return []uint64{}
	}
	return unique
}

func conftamerAttachServerRequest(req *Request) {
	if req.conftamerExchange != nil || !conftamerCaptureEnabled() {
		return
	}
	if req.ProtoMajor != 1 {
		conftamerFailCapture(errConftamerUnsupportedProtocol)
		return
	}
	exchange := conftamerNewExchange(req, nil)
	sequence := conftamerLogRequest(exchange, "receive_request")
	if sequence == 0 {
		return
	}
	req.conftamerExchange = exchange
	req.conftamerReceiveSequence = sequence
	req.conftamerReply = new(conftamerReplySources)
}

func conftamerNewClientExchange(req *Request) *conftamerExchange {
	if !conftamerCaptureEnabled() {
		return nil
	}
	return conftamerNewExchange(req, req.conftamerSources)
}

func conftamerNewExchange(req *Request, sources []uint64) *conftamerExchange {
	return &conftamerExchange{
		id:      conftamerExchangeCounter.Add(1),
		request: conftamerRequestSnapshot(req),
		sources: conftamerCanonicalSources(sources),
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

func conftamerLogRequest(exchange *conftamerExchange, kind string) uint64 {
	if exchange == nil {
		return 0
	}
	event := conftamerRequestEvent{Request: exchange.request}
	event.ExchangeID = exchange.id
	event.Kind = kind
	event.Sources = conftamerCanonicalSources(exchange.sources)
	return conftamerWriteRecord(&event.conftamerEnvelope, &event)
}

func conftamerLogResponse(exchange *conftamerExchange, kind string, statusCode int, sources []uint64) uint64 {
	if exchange == nil {
		return 0
	}
	event := conftamerResponseEvent{StatusCode: statusCode}
	event.ExchangeID = exchange.id
	event.Kind = kind
	event.Sources = conftamerCanonicalSources(sources)
	return conftamerWriteRecord(&event.conftamerEnvelope, &event)
}

func conftamerLogServerResponse(req *Request, statusCode int) {
	if req == nil || req.conftamerExchange == nil || req.conftamerReply == nil {
		return
	}
	sources := req.conftamerReply.freeze(req.conftamerReceiveSequence)
	conftamerLogResponse(req.conftamerExchange, "send_response", statusCode, sources)
}

// conftamerEnvelope identifies one record within a capture process.
type conftamerEnvelope struct {
	SchemaVersion int      `json:"schema_version"`
	CaptureID     string   `json:"capture_id"`
	ProcessID     string   `json:"process_id"`
	Seq           uint64   `json:"seq"`
	ExchangeID    uint64   `json:"exchange_id"`
	Kind          string   `json:"kind"`
	Sources       []uint64 `json:"sources"`
}

type conftamerRequestLabel struct {
	Method string  `json:"method"`
	Host   *string `json:"host"`
	Path   string  `json:"path"`
}

type conftamerRequestEvent struct {
	conftamerEnvelope
	Request conftamerRequestLabel `json:"request"`
}

type conftamerResponseEvent struct {
	conftamerEnvelope
	StatusCode int `json:"status_code"`
}

// conftamerExchange is immutable after publication to HTTP protocol workers.
type conftamerExchange struct {
	id      uint64
	request conftamerRequestLabel
	sources []uint64
}

type conftamerReplySources struct {
	mu      sync.Mutex
	sources []uint64
	frozen  bool
}

func (reply *conftamerReplySources) set(sources []uint64) bool {
	reply.mu.Lock()
	defer reply.mu.Unlock()
	if reply.frozen {
		return false
	}
	reply.sources = sources
	return true
}

func (reply *conftamerReplySources) freeze(requestSequence uint64) []uint64 {
	reply.mu.Lock()
	defer reply.mu.Unlock()
	reply.frozen = true
	sources := make([]uint64, 0, len(reply.sources)+1)
	sources = append(sources, reply.sources...)
	sources = append(sources, requestSequence)
	return conftamerCanonicalSources(sources)
}
