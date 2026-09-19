import json
from pathlib import Path

import pytest
from pydantic import ValidationError

from contexttrack.events import EVENT_ADAPTER, MessageLabel


ROOT = Path(__file__).parents[1]
FIXTURE = ROOT / "testdata/v2-chain.jsonl"
SCHEMA = ROOT / "event.schema.json"


def fixture_record(index: int) -> dict[str, object]:
    return json.loads(FIXTURE.read_text().splitlines()[index])


def assert_invalid(record: dict[str, object]) -> None:
    with pytest.raises(ValidationError):
        EVENT_ADAPTER.validate_json(json.dumps(record))


def test_fixture_covers_all_event_kinds():
    events = [
        EVENT_ADAPTER.validate_json(line)
        for line in FIXTURE.read_bytes().splitlines()
    ]

    assert {event.kind for event in events} == {
        "send_request",
        "receive_request",
        "send_response",
        "receive_response",
        "request_metadata",
    }


@pytest.mark.parametrize("status", ["200", True, 103])
def test_response_status_is_strict_and_terminal(status):
    record = json.loads(FIXTURE.read_text().splitlines()[3])
    record["status_code"] = status
    with pytest.raises(ValidationError):
        EVENT_ADAPTER.validate_json(json.dumps(record))


@pytest.mark.parametrize(
    ("record_index", "field"),
    [
        (0, "seq"),
        (0, "exchange_id"),
        (0, "context_id"),
    ],
)
@pytest.mark.parametrize("value", [True, "1", 0])
def test_counters_are_strict_positive_integers(record_index, field, value):
    record = fixture_record(record_index)
    record[field] = value

    assert_invalid(record)


@pytest.mark.parametrize("version", [1, 3, "2", 2.0, True])
def test_schema_version_is_exact_integer_two(version):
    record = fixture_record(0)
    record["schema_version"] = version

    assert_invalid(record)


def test_schema_version_is_required():
    record = fixture_record(0)
    del record["schema_version"]

    assert_invalid(record)


@pytest.mark.parametrize("kind", ["request_sent", "unknown"])
def test_unknown_event_kinds_are_rejected(kind):
    record = fixture_record(0)
    record["kind"] = kind

    assert_invalid(record)


@pytest.mark.parametrize("nested", [False, True])
def test_unknown_fields_are_rejected(nested):
    record = fixture_record(0)
    target = record["request"] if nested else record
    assert isinstance(target, dict)
    target["unexpected"] = "value"

    assert_invalid(record)


@pytest.mark.parametrize(
    ("record_index", "path"),
    [
        (0, ("context_id",)),
        (0, ("api_id",)),
        (0, ("request", "host")),
        (1, ("route",)),
        (1, ("api_id",)),
        (1, ("route", "full_pattern")),
    ],
)
def test_nullable_fields_are_still_required(record_index, path):
    record = fixture_record(record_index)
    target = record
    for component in path[:-1]:
        child = target[component]
        assert isinstance(child, dict)
        target = child
    del target[path[-1]]

    assert_invalid(record)


def test_send_request_requires_non_null_host():
    record = fixture_record(2)
    request = record["request"]
    assert isinstance(request, dict)
    request["host"] = None

    assert_invalid(record)


@pytest.mark.parametrize("dialect", ["servemux", "go_serve_mux_122", ""])
def test_route_dialect_is_closed(dialect):
    record = fixture_record(1)
    route = record["route"]
    assert isinstance(route, dict)
    route["dialect"] = dialect

    assert_invalid(record)


def test_metadata_requires_route_or_api_binding():
    record = fixture_record(1)
    record["route"] = None
    record["api_id"] = None

    assert_invalid(record)


@pytest.mark.parametrize("status", [101, 200, 999])
def test_terminal_response_boundaries_are_accepted(status):
    record = fixture_record(3)
    record["status_code"] = status

    event = EVENT_ADAPTER.validate_json(json.dumps(record))

    assert event.status_code == status


@pytest.mark.parametrize("status", [100, 102, 199, 1000])
def test_nonterminal_or_out_of_range_status_is_rejected(status):
    record = fixture_record(3)
    record["status_code"] = status

    assert_invalid(record)


def test_explicit_empty_path_and_null_context_and_api_are_accepted():
    record = fixture_record(0)
    request = record["request"]
    assert isinstance(request, dict)
    request["path"] = ""
    record["context_id"] = None
    record["api_id"] = None

    event = EVENT_ADAPTER.validate_json(json.dumps(record))

    assert event.request.path == ""
    assert event.context_id is None
    assert event.api_id is None


def test_ordinary_unicode_is_preserved():
    record = fixture_record(2)
    request = record["request"]
    assert isinstance(request, dict)
    request["method"] = "MÉTHODE"
    request["host"] = "módulo.example"
    request["path"] = "/雪"
    record["api_id"] = "例.example/api"

    event = EVENT_ADAPTER.validate_json(json.dumps(record))

    assert event.request.path == "/雪"
    assert event.api_id == "例.example/api"


@pytest.mark.parametrize("invalid_text", [b"\xff", br"\ud800"])
def test_invalid_utf8_and_escaped_surrogates_are_rejected(invalid_text):
    payload = json.dumps(fixture_record(0)).encode().replace(
        b"synthetic-chain", invalid_text
    )

    with pytest.raises(ValidationError):
        EVENT_ADAPTER.validate_json(payload)


def client_label(**changes) -> MessageLabel:
    fields = {
        "kind": "send_request",
        "api_id": "example.org/api",
        "method": "GET",
        "host": "service.test:8443",
        "path": "/items/7",
        "pattern": None,
        "pattern_dialect": None,
        "status_code": None,
    }
    fields.update(changes)
    return MessageLabel.model_validate(fields)


def server_label(**changes) -> MessageLabel:
    fields = {
        "kind": "receive_request",
        "api_id": None,
        "method": "GET",
        "host": None,
        "path": None,
        "pattern": "GET /items/{id}",
        "pattern_dialect": "go_serve_mux",
        "status_code": None,
    }
    fields.update(changes)
    return MessageLabel.model_validate(fields)


@pytest.mark.parametrize(
    ("left", "right"),
    [
        (client_label, lambda: client_label(host="other.test:8443")),
        (
            client_label,
            lambda: client_label(kind="receive_response", status_code=200),
        ),
        (
            server_label,
            lambda: server_label(pattern_dialect="httprouter"),
        ),
        (
            server_label,
            lambda: server_label(
                path="/items/7", pattern=None, pattern_dialect=None
            ),
        ),
    ],
)
def test_message_label_semantic_distinctions_are_hash_keys(left, right):
    left_label = left()
    right_label = right()

    assert left_label != right_label
    assert len({left_label, right_label}) == 2


@pytest.mark.parametrize(
    "changes",
    [
        {"host": None},
        {"path": None},
        {"pattern": "GET /items/{id}", "pattern_dialect": "go_serve_mux"},
        {"pattern_dialect": "go_serve_mux"},
    ],
)
def test_client_labels_require_only_host_and_path(changes):
    fields = client_label().model_dump()
    fields.update(changes)

    with pytest.raises(ValidationError):
        MessageLabel.model_validate(fields)


@pytest.mark.parametrize(
    "changes",
    [
        {"host": "service.test"},
        {"path": None, "pattern": None, "pattern_dialect": None},
        {"path": "/items/7"},
        {"pattern_dialect": None},
        {"pattern": None},
    ],
)
def test_server_labels_require_one_unambiguous_endpoint(changes):
    fields = server_label().model_dump()
    fields.update(changes)

    with pytest.raises(ValidationError):
        MessageLabel.model_validate(fields)


@pytest.mark.parametrize("kind", ["send_request", "receive_request"])
def test_request_labels_forbid_status(kind):
    factory = client_label if kind == "send_request" else server_label
    fields = factory().model_dump()
    fields["kind"] = kind
    fields["status_code"] = 200

    with pytest.raises(ValidationError):
        MessageLabel.model_validate(fields)


@pytest.mark.parametrize(
    ("kind", "factory"),
    [
        ("receive_response", client_label),
        ("send_response", server_label),
    ],
)
def test_response_labels_require_terminal_status(kind, factory):
    fields = factory().model_dump()
    fields["kind"] = kind

    with pytest.raises(ValidationError):
        MessageLabel.model_validate(fields)

    for invalid_status in (True, "200", 103):
        fields["status_code"] = invalid_status
        with pytest.raises(ValidationError):
            MessageLabel.model_validate(fields)

    fields["status_code"] = 200
    assert MessageLabel.model_validate(fields).status_code == 200


def test_message_label_fields_are_required_and_closed():
    missing = client_label().model_dump()
    del missing["api_id"]
    extra = client_label().model_dump()
    extra["exchange_id"] = 1

    with pytest.raises(ValidationError):
        MessageLabel.model_validate(missing)
    with pytest.raises(ValidationError):
        MessageLabel.model_validate(extra)


def test_generated_event_schema_is_current():
    assert json.loads(SCHEMA.read_text()) == EVENT_ADAPTER.json_schema()
