"""Consumer-facing validation, identity, and serialization contract."""

import json

import pytest
from pydantic import ValidationError

from contexttrack import models
from contexttrack.models import (
    EVENT_ADAPTER,
    ContextInfo,
    RequestReceived,
    RequestRouted,
    RequestSent,
    ResponseReceived,
    ResponseSent,
    SentRequestMessage,
)

CANONICAL_FIELDS = {
    "send_request": ("method", "path", "raw_query", "host"),
    "receive_request": ("method", "path", "raw_query"),
    "request_routed": ("method", "path", "pattern"),
    "send_response": ("method", "path", "status_code"),
    "receive_response": ("method", "path", "status_code"),
}
canonical_strings = [
    (kind, field)
    for kind, fields in CANONICAL_FIELDS.items()
    for field in fields
    if field != "status_code"
]
canonical_keys = {field for fields in CANONICAL_FIELDS.values() for field in fields}
canonical_forbidden = [
    (kind, field)
    for kind, fields in CANONICAL_FIELDS.items()
    for field in sorted(canonical_keys - set(fields))
]


@pytest.mark.parametrize("version", [True, 1.0, "1", 0, 2, None])
def test_version_is_exact_integer_one(normalized_sent, version):
    normalized_sent["schema_version"] = version
    with pytest.raises(ValidationError):
        EVENT_ADAPTER.validate_python(normalized_sent)
    with pytest.raises(ValidationError):
        EVENT_ADAPTER.validate_json(json.dumps(normalized_sent))


def test_pid_scopes_context_identity(normalized_sent):
    first = EVENT_ADAPTER.validate_python(normalized_sent)
    second = EVENT_ADAPTER.validate_python(normalized_sent | {"pid": 43})
    assert first.context_key == (42, "id:7")
    assert second.context_key == (43, "id:7")


@pytest.mark.parametrize("context", [{}, {"context_id": None}, {"context_id": ""}])
def test_missing_or_empty_context_id_has_no_group(normalized_sent, context):
    event = EVENT_ADAPTER.validate_python(normalized_sent | {"context": context})
    assert event.context_key is None


@pytest.mark.parametrize("kind", CANONICAL_FIELDS)
def test_canonical_path_cannot_be_empty(normalized_sent, kind):
    record = normalized_sent | {"kind": kind, "message": {"path": ""}}
    with pytest.raises(ValidationError):
        EVENT_ADAPTER.validate_python(record)


@pytest.mark.parametrize(
    "kind, event_type, message_type, message",
    [
        (
            "send_request",
            RequestSent,
            models.SentRequestMessage,
            {"method": "gEt", "path": "/é", "raw_query": "", "host": "Höst:80"},
        ),
        (
            "receive_request",
            RequestReceived,
            models.RequestMessage,
            {"method": "gEt", "path": "/é", "raw_query": "q=%2F&token=é"},
        ),
        (
            "request_routed",
            RequestRouted,
            models.RoutedRequestMessage,
            {"method": "gEt", "path": "/é", "pattern": "GET /{name}"},
        ),
        (
            "send_response",
            ResponseSent,
            models.ResponseMessage,
            {"method": "gEt", "path": "/é", "status_code": 200},
        ),
        (
            "receive_response",
            ResponseReceived,
            models.ResponseMessage,
            {"method": "gEt", "path": "/é", "status_code": 999},
        ),
    ],
)
def test_all_variants_have_typed_payloads_and_round_trip(
    normalized_sent, kind, event_type, message_type, message
):
    record = normalized_sent | {"kind": kind, "message": message}
    event = EVENT_ADAPTER.validate_python(record)
    assert type(event) is event_type
    assert type(event.message) is message_type
    assert event.kind == kind
    assert event.message.model_dump() == message
    assert event.context_key == (42, "id:7")
    assert event_type.model_validate(record) == event
    assert EVENT_ADAPTER.validate_json(event.model_dump_json()) == event


@pytest.mark.parametrize(
    "kind, message",
    [
        (
            "send_request",
            {"method": None, "path": None, "raw_query": None, "host": None},
        ),
        ("receive_request", {"method": None, "path": None, "raw_query": None}),
        ("request_routed", {"method": None, "path": None, "pattern": None}),
        ("send_response", {"method": None, "path": None, "status_code": None}),
        ("receive_response", {"method": None, "path": None, "status_code": None}),
    ],
)
def test_incomplete_events_serialize_explicit_nulls(normalized_sent, kind, message):
    record = normalized_sent | {"kind": kind, "context": {}, "message": {}}
    event = EVENT_ADAPTER.validate_python(record)
    expected = {
        "schema_version": 1,
        "kind": kind,
        "pid": 42,
        "context": {"context_id": None, "source": None, "type": None, "error": None},
        "api_id": None,
        "handler": None,
        "goroutine_id": None,
        "thread_id": None,
        "file": None,
        "line": None,
        "message": message,
    }
    assert json.loads(event.model_dump_json()) == expected
    assert EVENT_ADAPTER.validate_python(expected) == event
    assert event.context_key is None


@pytest.mark.parametrize(
    "field", ["schema_version", "kind", "pid", "context", "message"]
)
def test_required_envelope_fields_cannot_be_omitted(normalized_sent, field):
    del normalized_sent[field]
    with pytest.raises(ValidationError):
        EVENT_ADAPTER.validate_python(normalized_sent)


@pytest.mark.parametrize("field", ["context", "message"])
@pytest.mark.parametrize("value", [None, [], "object", True])
def test_required_nested_objects_cannot_be_null_or_wrong_type(
    normalized_sent, field, value
):
    normalized_sent[field] = value
    with pytest.raises(ValidationError):
        EVENT_ADAPTER.validate_python(normalized_sent)


@pytest.mark.parametrize("kind", ["Request sent", "unknown", "", None, True])
def test_unknown_or_noncanonical_kinds_are_rejected(normalized_sent, kind):
    normalized_sent["kind"] = kind
    with pytest.raises(ValidationError):
        EVENT_ADAPTER.validate_python(normalized_sent)


@pytest.mark.parametrize("target", ["envelope", "context", "message"])
def test_extra_fields_are_forbidden_at_every_level(normalized_sent, target):
    fields = normalized_sent if target == "envelope" else normalized_sent[target]
    fields["unknown"] = None
    with pytest.raises(ValidationError):
        EVENT_ADAPTER.validate_python(normalized_sent)


@pytest.mark.parametrize("kind, field", canonical_forbidden)
def test_message_fields_are_limited_to_the_event_kind(normalized_sent, kind, field):
    record = normalized_sent | {"kind": kind, "message": {field: None}}
    with pytest.raises(ValidationError):
        EVENT_ADAPTER.validate_python(record)


@pytest.mark.parametrize("kind, field", canonical_strings)
@pytest.mark.parametrize("value", [True, 42, 1.5, [], {}])
def test_message_strings_reject_non_strings(normalized_sent, kind, field, value):
    record = normalized_sent | {"kind": kind, "message": {field: value}}
    with pytest.raises(ValidationError):
        EVENT_ADAPTER.validate_python(record)


@pytest.mark.parametrize("kind", ["send_response", "receive_response"])
@pytest.mark.parametrize("status", [None, 0, 200, 999])
def test_response_status_is_nullable_and_nonnegative(normalized_sent, kind, status):
    record = normalized_sent | {"kind": kind, "message": {"status_code": status}}
    event = EVENT_ADAPTER.validate_python(record)
    assert isinstance(event, (ResponseSent, ResponseReceived))
    assert event.message.status_code == status
    assert json.loads(event.model_dump_json())["message"]["status_code"] == status


@pytest.mark.parametrize("kind", ["send_response", "receive_response"])
@pytest.mark.parametrize("status", [True, False, "200", 200.0, -1])
def test_canonical_status_rejects_coercion_and_negatives(normalized_sent, kind, status):
    record = normalized_sent | {"kind": kind, "message": {"status_code": status}}
    with pytest.raises(ValidationError):
        EVENT_ADAPTER.validate_python(record)


def test_message_strings_are_not_normalized(normalized_sent):
    record = normalized_sent | {
        "message": {
            "method": " gEt ",
            "host": "Höst:80",
            "path": "/a/../é%2Fb?query=literal",
            "raw_query": "q=%2F&token=é",
        },
    }
    event = EVENT_ADAPTER.validate_python(record)
    assert event.message.model_dump() == record["message"]
    assert "Höst:80" in event.model_dump_json(ensure_ascii=False)


@pytest.mark.parametrize(
    "target, field",
    [("event", "pid"), ("context", "context_id"), ("message", "path")],
)
def test_events_and_nested_models_are_immutable(normalized_sent, target, field):
    event = EVENT_ADAPTER.validate_python(normalized_sent)
    instance = event if target == "event" else getattr(event, target)
    with pytest.raises(ValidationError) as error:
        setattr(instance, field, None)
    assert error.value.errors()[0]["type"] == "frozen_instance"


@pytest.mark.parametrize("target", ["envelope", "context", "message"])
def test_constructed_model_instances_are_revalidated(normalized_sent, target):
    event = EVENT_ADAPTER.validate_python(normalized_sent)
    if target == "envelope":
        invalid = event.model_copy(update={"pid": True})
    elif target == "context":
        invalid = event.model_copy(
            update={"context": ContextInfo.model_construct(context_id=42)}
        )
    else:
        invalid = event.model_copy(
            update={"message": SentRequestMessage.model_construct(path="")}
        )
    with pytest.raises(ValidationError):
        EVENT_ADAPTER.validate_python(invalid)
