package http

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
