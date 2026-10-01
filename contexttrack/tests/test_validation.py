"""Metadata validation shared by raw and normalized event boundaries."""

import json

import pytest
from pydantic import ValidationError


@pytest.mark.parametrize("field", ["pid", "goroutine_id", "thread_id", "line"])
@pytest.mark.parametrize("value", [True, "42", 42.0])
def test_integer_metadata_is_strict(boundary, field, value):
    validate, record = boundary
    with pytest.raises(ValidationError):
        validate(record | {field: value})


@pytest.mark.parametrize("pid", [None, [], {}])
def test_pid_is_required_to_be_an_integer(boundary, pid):
    validate, record = boundary
    with pytest.raises(ValidationError):
        validate(record | {"pid": pid})


@pytest.mark.parametrize("field", ["pid", "goroutine_id", "thread_id", "line"])
@pytest.mark.parametrize("value", [0, -1])
def test_integer_metadata_preserves_zero_and_negative_values(boundary, field, value):
    validate, record = boundary
    event = validate(record | {field: value})
    assert getattr(event, field) == value
    assert json.loads(event.model_dump_json())[field] == value
    assert event.context_key == (value if field == "pid" else 42, "id:7")


@pytest.mark.parametrize(
    "field", ["api_id", "handler", "file", "goroutine_id", "thread_id", "line"]
)
def test_optional_envelope_fields_accept_null(boundary, field):
    validate, record = boundary
    event = validate(record | {field: None})
    assert getattr(event, field) is None
    assert json.loads(event.model_dump_json())[field] is None


@pytest.mark.parametrize("field", ["api_id", "handler", "file"])
@pytest.mark.parametrize("value", [True, 42, 1.5, [], {}])
def test_optional_envelope_strings_are_strict(boundary, field, value):
    validate, record = boundary
    with pytest.raises(ValidationError):
        validate(record | {field: value})


@pytest.mark.parametrize("field", ["context_id", "source", "type", "error"])
@pytest.mark.parametrize("value", [True, 42, 1.5, [], {}])
def test_context_strings_are_strict(boundary, field, value):
    validate, record = boundary
    record["context"][field] = value
    with pytest.raises(ValidationError):
        validate(record)


@pytest.mark.parametrize("field", ["context_id", "source", "type", "error"])
def test_context_fields_accept_explicit_null(boundary, field):
    validate, record = boundary
    record["context"][field] = None
    assert getattr(validate(record).context, field) is None


@pytest.mark.parametrize("handler", [" handler ", " handler\n"])
def test_metadata_and_context_diagnostics_are_preserved(boundary, handler):
    validate, record = boundary
    metadata = {
        "api_id": " API\n",
        "handler": handler,
        "file": "/gö/src/net/http/transport.go",
        "line": 640,
        "goroutine_id": 8,
        "thread_id": 0,
    }
    context = {
        "context_id": " ",
        "source": "req.Context()",
        "type": "context.Context",
        "error": " diagnostic\n",
    }
    event = validate(record | metadata | {"context": context})
    for field, value in metadata.items():
        assert getattr(event, field) == value
    assert event.model_dump(include=set(metadata)) == metadata
    assert event.context.model_dump() == context
    assert event.context_key == (42, " ")
