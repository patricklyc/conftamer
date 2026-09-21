import json
from pathlib import Path

import pytest
from pydantic import ValidationError

from contexttrack.events import EVENT_ADAPTER, MessageLabel

ROOT = Path(__file__).parents[1]
FIXTURE = ROOT / "testdata/v4-chain.jsonl"
SCHEMA = ROOT / "event.schema.json"


def fixture_record(index: int) -> dict[str, object]:
    return json.loads(FIXTURE.read_text().splitlines()[index])


def assert_invalid(record: dict[str, object]) -> None:
    with pytest.raises(ValidationError):
        EVENT_ADAPTER.validate_json(json.dumps(record))


def test_fixture_has_four_kinds_and_two_exact_shapes():
    records = [fixture_record(index) for index in range(4)]
    events = [EVENT_ADAPTER.validate_python(record) for record in records]

    assert [event.kind for event in events] == [
        "receive_request",
        "send_request",
        "receive_response",
        "send_response",
    ]
    envelope = {
        "schema_version",
        "capture_id",
        "process_id",
        "seq",
        "exchange_id",
        "kind",
        "sources",
    }
    assert set(records[0]) == envelope | {"request"}
    assert set(records[2]) == envelope | {"status_code"}


@pytest.mark.parametrize("version", [1, 2, 3, "4", 4.0, True])
def test_schema_version_is_exact_integer_four(version):
    record = fixture_record(0)
    record["schema_version"] = version

    assert_invalid(record)


@pytest.mark.parametrize("field", ["seq", "exchange_id"])
@pytest.mark.parametrize("value", [True, "1", 0, 2**64])
def test_counters_are_strict_unsigned_nonzero(field, value):
    record = fixture_record(0)
    record[field] = value

    assert_invalid(record)


@pytest.mark.parametrize("value", [True, "1", 0, 2**64])
def test_source_counters_are_strict_unsigned_nonzero(value):
    record = fixture_record(1)
    record["sources"] = [value]

    assert_invalid(record)


@pytest.mark.parametrize("sources", [[1, 1], [3, 1]])
def test_sources_must_be_sorted_and_unique(sources):
    record = fixture_record(3)
    record["sources"] = sources

    assert_invalid(record)


@pytest.mark.parametrize("index", [0, 2])
def test_receive_records_require_empty_sources(index):
    record = fixture_record(index)
    record["sources"] = [1]

    assert_invalid(record)


@pytest.mark.parametrize("status", ["200", True, 100, 103, 199, 1000])
def test_response_status_is_strict_terminal(status):
    record = fixture_record(2)
    record["status_code"] = status

    assert_invalid(record)


@pytest.mark.parametrize("status", [101, 200, 999])
def test_terminal_response_boundaries_are_accepted(status):
    record = fixture_record(2)
    record["status_code"] = status

    assert EVENT_ADAPTER.validate_python(record).status_code == status


@pytest.mark.parametrize("kind", ["request_metadata", "request_sent", "unknown"])
def test_other_event_kinds_are_rejected(kind):
    record = fixture_record(0)
    record["kind"] = kind

    assert_invalid(record)


@pytest.mark.parametrize(
    ("index", "field", "value"),
    [
        (0, "api_id", None),
        (0, "route", None),
        (0, "status_code", 200),
        (0, "context_id", 7),
        (2, "context_id", None),
        (2, "request", {}),
    ],
)
def test_other_variant_and_removed_fields_are_rejected(index, field, value):
    record = fixture_record(index)
    record[field] = value

    assert_invalid(record)


def test_unknown_nested_request_fields_are_rejected():
    record = fixture_record(0)
    request = record["request"]
    assert isinstance(request, dict)
    request["unexpected"] = "value"

    assert_invalid(record)


@pytest.mark.parametrize(
    ("index", "path"),
    [(0, ("sources",)), (0, ("request", "host"))],
)
def test_required_fields_cannot_be_omitted(index, path):
    record = fixture_record(index)
    target = record
    for component in path[:-1]:
        child = target[component]
        assert isinstance(child, dict)
        target = child
    del target[path[-1]]

    assert_invalid(record)


def test_send_request_requires_host():
    record = fixture_record(1)
    request = record["request"]
    assert isinstance(request, dict)
    request["host"] = None

    assert_invalid(record)


def test_empty_path_and_unicode_are_preserved():
    record = fixture_record(1)
    request = record["request"]
    assert isinstance(request, dict)
    request.update(method="MÉTHODE", host="módulo.example", path="")

    event = EVENT_ADAPTER.validate_json(json.dumps(record))

    assert event.request.method == "MÉTHODE"
    assert event.request.host == "módulo.example"
    assert event.request.path == ""
    assert event.sources == [1]


def test_v1_v2_v3_and_former_metadata_records_are_rejected():
    for version in (1, 2, 3):
        record = fixture_record(0)
        record["schema_version"] = version
        assert_invalid(record)
    metadata: dict[str, object] = {
        "schema_version": 4,
        "capture_id": "capture",
        "process_id": "a" * 32,
        "seq": 1,
        "exchange_id": 1,
        "kind": "request_metadata",
        "sources": [],
        "route": None,
        "api_id": "example.org/api",
    }
    assert_invalid(metadata)


def message_label(**changes: object) -> MessageLabel:
    fields: dict[str, object] = {
        "kind": "send_request",
        "method": "GET",
        "host": "service.test:8443",
        "path": "/items/7",
        "status_code": None,
    }
    fields.update(changes)
    return MessageLabel.model_validate(fields)


def test_message_label_has_five_fields_and_is_hashable():
    label = message_label()

    assert label.model_dump() == {
        "kind": "send_request",
        "method": "GET",
        "host": "service.test:8443",
        "path": "/items/7",
        "status_code": None,
    }
    assert {label, message_label(host="other.test")} == {
        label,
        message_label(host="other.test"),
    }


@pytest.mark.parametrize(
    "changes",
    [
        {"host": None},
        {"path": ""},
        {"path": None},
        {"method": ""},
        {"api_id": None},
        {"pattern": "/items/{id}"},
    ],
)
def test_client_label_requires_only_nonempty_host_and_path(changes):
    fields = message_label().model_dump()
    fields.update(changes)

    with pytest.raises(ValidationError):
        MessageLabel.model_validate(fields)


def test_server_label_requires_null_host_and_concrete_path():
    server = message_label(kind="receive_request", host=None)
    assert server.path == "/items/7"

    for host in ("service.test", ""):
        fields = server.model_dump()
        fields["host"] = host
        with pytest.raises(ValidationError):
            MessageLabel.model_validate(fields)


@pytest.mark.parametrize("kind", ["send_request", "receive_request"])
def test_request_labels_forbid_status(kind):
    fields = message_label(
        kind=kind,
        host=None if kind == "receive_request" else "service.test",
    ).model_dump()
    fields["status_code"] = 200

    with pytest.raises(ValidationError):
        MessageLabel.model_validate(fields)


@pytest.mark.parametrize(
    ("kind", "host"),
    [("receive_response", "service.test"), ("send_response", None)],
)
def test_response_labels_require_strict_terminal_status(kind, host):
    fields = message_label().model_dump()
    fields.update(kind=kind, host=host)
    for status in (None, True, "200", 103):
        fields["status_code"] = status
        with pytest.raises(ValidationError):
            MessageLabel.model_validate(fields)
    fields["status_code"] = 204
    assert MessageLabel.model_validate(fields).status_code == 204


def test_generated_event_schema_is_current():
    assert json.loads(SCHEMA.read_text()) == EVENT_ADAPTER.json_schema()
