"""Record-local normalization and strict raw-format boundary regressions."""

from copy import deepcopy

import pytest
from pydantic import ValidationError

from contexttrack.models import (
    RequestReceived,
    RequestRouted,
    RequestSent,
    ResponseReceived,
    ResponseSent,
)
from contexttrack.normalize import normalize_record

RAW_KINDS = (
    "Request sent",
    "Request received",
    "Request routed",
    "Response sent",
    "Response received",
)

RESPONSE_STATUSES = (
    ("Response sent", "code"),
    ("Response received", "resp.StatusCode"),
)


def test_outbound_label_precedence_and_no_input_mutation(raw_sent):
    raw_sent["message"]["req.Method"] = "POST"
    raw_sent["message"]["req.URL.Host"] = "other.example"
    raw_sent["message"]["req.URL.Path"] = "/different"
    original = deepcopy(raw_sent)
    event = normalize_record(raw_sent)
    assert isinstance(event, RequestSent)
    assert (event.message.method, event.message.host, event.message.path) == (
        "gEt",
        "Höst:80",
        "/",
    )
    assert event.message.raw_query == ""
    assert event.api_id == " API\n"
    assert event.thread_id == 0
    assert event.context_key == (42, "id:7")
    assert raw_sent == original


def test_missing_request_id_is_not_invented_from_message(raw_sent):
    del raw_sent["request_id"]
    event = normalize_record(raw_sent)
    assert isinstance(event, RequestSent)
    assert (event.message.method, event.message.host, event.message.path) == (
        None,
        None,
        None,
    )
    assert event.message.raw_query == ""


def test_response_is_normalized_without_association():
    event = normalize_record(
        {
            "kind": "Response received",
            "pid": 42,
            "context": {"error": "missing context ID"},
            "message": {
                "req.Method": "gEt",
                "req.URL.Path": "",
                "resp.StatusCode": "0200",
            },
        }
    )
    assert isinstance(event, ResponseReceived)
    assert event.kind == "receive_response"
    assert event.message.method == "gEt"
    assert event.message.path == "/"
    assert event.message.status_code == 200
    assert event.api_id is None
    assert event.handler is None
    assert event.context_key is None
    assert event.context.error == "missing context ID"


@pytest.mark.parametrize(
    "raw_kind, kind, event_type, raw_fields, normalized_fields",
    [
        (
            "Request sent",
            "send_request",
            RequestSent,
            {"req.URL.Host": "Höst:80", "req.URL.RawQuery": "q=é"},
            {"host": "Höst:80", "raw_query": "q=é"},
        ),
        (
            "Request received",
            "receive_request",
            RequestReceived,
            {"req.URL.RawQuery": "q=é"},
            {"raw_query": "q=é"},
        ),
        (
            "Request routed",
            "request_routed",
            RequestRouted,
            {"pattern": "GET /{name}"},
            {"pattern": "GET /{name}"},
        ),
        (
            "Response sent",
            "send_response",
            ResponseSent,
            {"code": "0200"},
            {"status_code": 200},
        ),
        (
            "Response received",
            "receive_response",
            ResponseReceived,
            {"resp.StatusCode": "999"},
            {"status_code": 999},
        ),
    ],
)
def test_each_kind_maps_to_its_typed_payload(
    raw_kind, kind, event_type, raw_fields, normalized_fields
):
    record = {
        "kind": raw_kind,
        "pid": 42,
        "message": {"req.Method": "gEt", "req.URL.Path": "/é"} | raw_fields,
    }
    if raw_kind == "Request sent":
        record["request_id"] = {"method": "gEt", "host": "Höst:80", "path": "/é"}
    original = deepcopy(record)
    event = normalize_record(record)
    assert type(event) is event_type
    assert event.schema_version == 1
    assert event.kind == kind
    expected_message = {"method": "gEt", "path": "/é"} | normalized_fields
    assert event.message.model_dump() == expected_message
    assert record == original


@pytest.mark.parametrize("kind", RAW_KINDS)
def test_empty_path_becomes_slash_for_every_kind(kind):
    record = {"kind": kind, "pid": 42, "message": {"req.URL.Path": ""}}
    if kind == "Request sent":
        record["request_id"] = {"path": ""}
    assert normalize_record(record).message.path == "/"


@pytest.mark.parametrize("kind", RAW_KINDS)
def test_incomplete_message_is_retained_without_warnings(kind, recwarn, capsys):
    event = normalize_record({"kind": kind, "pid": 42, "message": {}})
    assert all(value is None for value in event.message.model_dump().values())
    assert event.context.model_dump() == {
        "context_id": None,
        "source": None,
        "type": None,
        "error": None,
    }
    assert event.context_key is None
    assert not recwarn
    assert capsys.readouterr() == ("", "")


@pytest.mark.parametrize(
    "request_id, labels",
    [
        (None, (None, None, None)),
        ({}, (None, None, None)),
        ({"host": "Höst:80"}, (None, "Höst:80", None)),
        ({"method": "", "host": "", "path": ""}, ("", "", "/")),
    ],
)
def test_null_empty_or_partial_request_id_never_falls_back(
    raw_sent, request_id, labels
):
    raw_sent["request_id"] = request_id
    event = normalize_record(raw_sent)
    assert isinstance(event, RequestSent)
    assert (event.message.method, event.message.host, event.message.path) == labels


@pytest.mark.parametrize("kind", RAW_KINDS)
def test_null_request_id_is_legal_on_every_kind(kind):
    event = normalize_record(
        {"kind": kind, "pid": 42, "message": {}, "request_id": None}
    )
    assert all(value is None for value in event.message.model_dump().values())


@pytest.mark.parametrize("kind", RAW_KINDS[1:])
@pytest.mark.parametrize("request_id", [{}, {"method": "GET"}])
def test_non_null_request_id_is_forbidden_outside_sent_requests(kind, request_id):
    with pytest.raises(ValidationError, match="request_id"):
        normalize_record(
            {"kind": kind, "pid": 42, "message": {}, "request_id": request_id}
        )


@pytest.mark.parametrize(
    "context", [None, {}, {"context_id": None}, {"context_id": ""}]
)
def test_absent_context_identity_never_creates_a_shared_group(raw_sent, context):
    raw_sent["context"] = context
    event = normalize_record(raw_sent)
    assert event.context.context_id == ("" if context == {"context_id": ""} else None)
    assert event.context_key is None


@pytest.mark.parametrize(
    "field", ["api_id", "handler", "file", "goroutine_id", "thread_id", "line"]
)
def test_optional_envelope_fields_accept_null(raw_sent, field):
    event = normalize_record(raw_sent | {field: None})
    assert getattr(event, field) is None


@pytest.mark.parametrize("field", ["pid", "goroutine_id", "thread_id", "line"])
@pytest.mark.parametrize("value", [0, -1])
def test_integer_metadata_preserves_zero_and_negative_values(raw_sent, field, value):
    event = normalize_record(raw_sent | {field: value})
    assert getattr(event, field) == value


@pytest.mark.parametrize("field", ["pid", "goroutine_id", "thread_id", "line"])
@pytest.mark.parametrize("value", [True, "42", 42.0])
def test_integer_metadata_is_not_coerced(raw_sent, field, value):
    with pytest.raises(ValidationError):
        normalize_record(raw_sent | {field: value})


@pytest.mark.parametrize("pid", [None, [], {}])
def test_pid_is_required_to_be_an_integer(raw_sent, pid):
    with pytest.raises(ValidationError):
        normalize_record(raw_sent | {"pid": pid})


@pytest.mark.parametrize("field", ["api_id", "handler", "file"])
@pytest.mark.parametrize("value", [True, 42, 1.5, [], {}])
def test_optional_envelope_strings_are_not_coerced(raw_sent, field, value):
    with pytest.raises(ValidationError):
        normalize_record(raw_sent | {field: value})


@pytest.mark.parametrize("field", ["context_id", "source", "type", "error"])
@pytest.mark.parametrize("value", [True, 42, 1.5, [], {}])
def test_context_fields_are_nullable_strings_not_coerced_values(raw_sent, field, value):
    raw_sent["context"][field] = value
    with pytest.raises(ValidationError):
        normalize_record(raw_sent)


@pytest.mark.parametrize("field", ["context_id", "source", "type", "error"])
def test_context_fields_accept_explicit_null(raw_sent, field):
    raw_sent["context"][field] = None
    assert getattr(normalize_record(raw_sent).context, field) is None


@pytest.mark.parametrize("field", ["kind", "pid", "message"])
def test_required_raw_fields_cannot_be_omitted(raw_sent, field):
    del raw_sent[field]
    with pytest.raises(ValidationError):
        normalize_record(raw_sent)


@pytest.mark.parametrize("record", [None, [], True, 42, "{}"])
def test_non_object_records_are_rejected(record):
    with pytest.raises(ValidationError):
        normalize_record(record)


@pytest.mark.parametrize("value", [None, [], "object", True, 42])
def test_message_must_be_an_object_even_for_incomplete_events(raw_sent, value):
    with pytest.raises(ValidationError):
        normalize_record(raw_sent | {"message": value})


@pytest.mark.parametrize("field", ["context", "request_id"])
@pytest.mark.parametrize("value", [[], "object", True, 42])
def test_optional_nested_fields_are_objects_when_non_null(raw_sent, field, value):
    with pytest.raises(ValidationError):
        normalize_record(raw_sent | {field: value})


@pytest.mark.parametrize("kind", ["unknown", "send_request", "", None, True, 42])
def test_unknown_or_canonical_kinds_are_not_raw_kinds(raw_sent, kind):
    with pytest.raises(ValidationError):
        normalize_record(raw_sent | {"kind": kind})


@pytest.mark.parametrize("target", ["envelope", "message", "context", "request_id"])
def test_unknown_fields_are_forbidden_at_every_raw_level(raw_sent, target):
    fields = raw_sent if target == "envelope" else raw_sent[target]
    fields["unknown"] = None
    with pytest.raises(ValidationError, match="unknown"):
        normalize_record(raw_sent)


@pytest.mark.parametrize(
    "field", ["method", "host", "path", "raw_query", "status_code"]
)
def test_internal_message_names_are_not_accepted_as_raw_aliases(raw_sent, field):
    raw_sent["message"][field] = "200"
    with pytest.raises(ValidationError):
        normalize_record(raw_sent)


@pytest.mark.parametrize(
    "kind, field",
    [
        ("Request sent", "req.Method"),
        ("Request sent", "req.URL.Host"),
        ("Request sent", "req.URL.Path"),
        ("Request sent", "req.URL.RawQuery"),
        ("Request received", "req.Method"),
        ("Request received", "req.URL.Path"),
        ("Request received", "req.URL.RawQuery"),
        ("Request routed", "req.Method"),
        ("Request routed", "req.URL.Path"),
        ("Request routed", "pattern"),
        ("Response sent", "req.Method"),
        ("Response sent", "req.URL.Path"),
        ("Response sent", "code"),
        ("Response received", "req.Method"),
        ("Response received", "req.URL.Path"),
        ("Response received", "resp.StatusCode"),
    ],
)
@pytest.mark.parametrize("value", [None, True, 42, 1.5, [], {}])
def test_present_message_fields_must_be_strings_even_when_unused(kind, field, value):
    with pytest.raises(ValidationError):
        normalize_record({"kind": kind, "pid": 42, "message": {field: value}})


@pytest.mark.parametrize("field", ["method", "host", "path"])
@pytest.mark.parametrize("value", [None, True, 42, 1.5, [], {}])
def test_present_request_labels_must_be_strings(raw_sent, field, value):
    raw_sent["request_id"][field] = value
    with pytest.raises(ValidationError):
        normalize_record(raw_sent)


@pytest.mark.parametrize(
    "kind, field",
    [
        ("Request sent", "pattern"),
        ("Request sent", "code"),
        ("Request sent", "resp.StatusCode"),
        ("Request received", "req.URL.Host"),
        ("Request received", "pattern"),
        ("Request received", "code"),
        ("Request received", "resp.StatusCode"),
        ("Request routed", "req.URL.Host"),
        ("Request routed", "req.URL.RawQuery"),
        ("Request routed", "code"),
        ("Request routed", "resp.StatusCode"),
        ("Response sent", "req.URL.Host"),
        ("Response sent", "req.URL.RawQuery"),
        ("Response sent", "pattern"),
        ("Response sent", "resp.StatusCode"),
        ("Response received", "req.URL.Host"),
        ("Response received", "req.URL.RawQuery"),
        ("Response received", "pattern"),
        ("Response received", "code"),
    ],
)
def test_message_fields_of_other_kinds_are_forbidden(kind, field):
    with pytest.raises(ValidationError):
        normalize_record({"kind": kind, "pid": 42, "message": {field: "200"}})


@pytest.mark.parametrize("kind, field", RESPONSE_STATUSES)
@pytest.mark.parametrize(
    "status, expected",
    [
        ("0", 0),
        ("000", 0),
        ("103", 103),
        ("0200", 200),
        ("999", 999),
        ("1000", 1000),
        ("٢٠٠", 200),
    ],
)
def test_decimal_status_strings_are_converted_without_http_range_limits(
    kind, field, status, expected
):
    event = normalize_record({"kind": kind, "pid": 42, "message": {field: status}})
    assert isinstance(event, (ResponseSent, ResponseReceived))
    assert event.message.status_code == expected
    assert event.message.method is None
    assert event.message.path is None


@pytest.mark.parametrize("kind, field", RESPONSE_STATUSES)
@pytest.mark.parametrize(
    "status",
    [
        "",
        " ",
        " 200",
        "200 ",
        "200\n",
        "+200",
        "-1",
        "-0",
        "200.0",
        "2e2",
        "0xC8",
        "²",
        "NaN",
        "Infinity",
    ],
)
def test_non_decimal_status_strings_are_rejected(kind, field, status):
    with pytest.raises(ValueError, match="decimal string"):
        normalize_record({"kind": kind, "pid": 42, "message": {field: status}})


@pytest.mark.parametrize(
    "context", [{"root_addr": "0x1234"}, {"context_id": "id:7", "root_addr": "0x1234"}]
)
def test_historical_context_root_addresses_are_rejected(raw_sent, context):
    with pytest.raises(ValidationError, match="root_addr"):
        normalize_record(raw_sent | {"context": context})


def test_already_normalized_records_are_not_raw_input(normalized_sent):
    with pytest.raises(ValidationError):
        normalize_record(normalized_sent)


@pytest.mark.parametrize("kind", ["Request sent", "Request received"])
@pytest.mark.parametrize("query", ["", "q=%2F&token=é"])
def test_query_preserves_empty_and_unicode_evidence(kind, query):
    event = normalize_record(
        {"kind": kind, "pid": 42, "message": {"req.URL.RawQuery": query}}
    )
    assert isinstance(event, (RequestSent, RequestReceived))
    assert event.message.raw_query == query


@pytest.mark.parametrize("kind", ["Request sent", "Request received"])
def test_missing_query_is_not_an_empty_query(kind):
    event = normalize_record({"kind": kind, "pid": 42, "message": {}})
    assert isinstance(event, (RequestSent, RequestReceived))
    assert event.message.raw_query is None


@pytest.mark.parametrize("pattern", ["GET /items/{id}", ":name", "*path", ""])
def test_route_pattern_syntax_is_not_reconstructed_or_rewritten(pattern):
    event = normalize_record(
        {
            "kind": "Request routed",
            "pid": 42,
            "message": {
                "req.Method": "GET",
                "req.URL.Path": "/items/7",
                "pattern": pattern,
            },
        }
    )
    assert isinstance(event, RequestRouted)
    assert event.message.path == "/items/7"
    assert event.message.pattern == pattern


def test_strings_context_diagnostics_and_metadata_are_preserved(raw_sent):
    record = raw_sent | {
        "handler": " handler\n",
        "file": "/gö/src/net/http/transport.go",
        "line": 640,
        "goroutine_id": 8,
        "context": {
            "context_id": " ",
            "source": "req.Context()",
            "type": "context.Context",
            "error": " diagnostic\n",
        },
        "request_id": {
            "method": " gEt ",
            "host": "Höst:80",
            "path": "/a/../é%2Fb?query=literal",
        },
        "message": {"req.URL.RawQuery": "q=%2F&token=é"},
    }
    event = normalize_record(record)
    assert isinstance(event, RequestSent)
    assert event.message.model_dump() == {
        "method": " gEt ",
        "host": "Höst:80",
        "path": "/a/../é%2Fb?query=literal",
        "raw_query": "q=%2F&token=é",
    }
    assert event.context.model_dump() == record["context"]
    assert event.context_key == (42, " ")
    assert (
        event.api_id,
        event.handler,
        event.file,
        event.line,
        event.goroutine_id,
    ) == (
        " API\n",
        " handler\n",
        "/gö/src/net/http/transport.go",
        640,
        8,
    )


def test_failed_validation_does_not_mutate_input(raw_sent):
    raw_sent["request_id"]["path"] = None
    original = deepcopy(raw_sent)
    with pytest.raises(ValidationError):
        normalize_record(raw_sent)
    assert raw_sent == original
