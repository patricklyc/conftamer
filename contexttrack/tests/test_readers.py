"""Strict, lazy JSONL readers with immutable physical locations."""

import json
from dataclasses import FrozenInstanceError

import pytest
from pydantic import ValidationError

from contexttrack import LocatedEvent
from contexttrack import io as event_io
from contexttrack.io import EventFileError, iter_events, iter_raw_events

READERS = (iter_raw_events, iter_events)


def test_raw_reader_normalizes_in_memory(tmp_path, raw_sent):
    source = tmp_path / "raw.jsonl"
    source.write_text(
        "\n" + json.dumps(raw_sent, ensure_ascii=False) + "\n", encoding="utf-8"
    )
    before = source.read_bytes()
    [record] = iter_raw_events(source)
    assert isinstance(record, LocatedEvent)
    assert record.event.kind == "send_request"
    assert record.event.message.path == "/"
    assert record.location == f"{source}:2"
    assert source.read_bytes() == before
    assert sorted(path.name for path in tmp_path.iterdir()) == [source.name]


def test_raw_record_error_has_raw_physical_line(tmp_path, raw_sent):
    source = tmp_path / "raw.jsonl"
    bad = raw_sent | {"kind": "unknown"}
    source.write_text(json.dumps(raw_sent) + "\n\n" + json.dumps(bad), encoding="utf-8")
    records = iter_raw_events(source)
    assert next(records).line == 1
    with pytest.raises(EventFileError, match=rf"{source}:3:"):
        next(records)


def test_reader_location_is_frozen_and_not_serialized(tmp_path, normalized_sent):
    source = tmp_path / "events.jsonl"
    source.write_text("\n" + json.dumps(normalized_sent), encoding="utf-8")
    [record] = iter_events(str(source))
    assert record.path == source
    assert record.line == 2
    with pytest.raises(FrozenInstanceError):
        record.__setattr__("line", 99)
    assert "location" not in record.event.model_dump()


@pytest.mark.parametrize("reader", READERS)
@pytest.mark.parametrize("text", ["", "\n \t\r\n\n"])
def test_empty_and_blank_only_files_are_valid(tmp_path, reader, text):
    source = tmp_path / "empty.jsonl"
    source.write_text(text, encoding="utf-8")
    assert list(reader(source)) == []


@pytest.mark.parametrize("reader", READERS)
def test_readers_open_lazily_and_accept_final_line_without_newline(
    tmp_path, raw_sent, normalized_sent, reader
):
    source = tmp_path / "events.jsonl"
    records = reader(source)  # The path does not exist until iteration begins.
    value = raw_sent if reader is iter_raw_events else normalized_sent
    source.write_text("\n \t\n" + json.dumps(value), encoding="utf-8")
    [record] = records
    assert record.line == 3
    assert record.event.kind == "send_request"


@pytest.mark.parametrize("reader", READERS)
@pytest.mark.parametrize(
    "bad_line, cause_type, reason",
    [
        (b"{", json.JSONDecodeError, ""),
        (b"\xff", UnicodeDecodeError, "utf-8"),
        (b"\xef\xbb\xbf{}", json.JSONDecodeError, "BOM"),
        (b'{"pid":42,"pid":43}', ValueError, "duplicate JSON key"),
        (
            b'{"context":{"context_id":"a","context_id":"b"}}',
            ValueError,
            "duplicate JSON key",
        ),
        (b'{"a":1,"\\u0061":2}', ValueError, "duplicate JSON key"),
        (b'{"pid":NaN}', ValueError, "invalid JSON constant"),
        (b'{"pid":Infinity}', ValueError, "invalid JSON constant"),
        (b'{"pid":-Infinity}', ValueError, "invalid JSON constant"),
        (b"[]", ValidationError, ""),
        (b"null", ValidationError, ""),
        (b"true", ValidationError, ""),
        (b"42", ValidationError, ""),
        (b'"record"', ValidationError, ""),
    ],
)
def test_input_errors_keep_physical_line_and_original_cause(
    tmp_path, raw_sent, normalized_sent, reader, bad_line, cause_type, reason
):
    source = tmp_path / "events.jsonl"
    value = raw_sent if reader is iter_raw_events else normalized_sent
    data = json.dumps(value).encode("utf-8") + b"\n\n" + bad_line
    source.write_bytes(data)
    records = reader(source)
    assert next(records).line == 1  # A later bad record is not read eagerly.
    with pytest.raises(EventFileError) as caught:
        next(records)
    error = caught.value
    assert error.path == source
    assert error.line == 3
    assert error.reason
    assert str(error).startswith(f"{source}:3:")
    assert reason in error.reason
    assert isinstance(error.__cause__, cause_type)
    assert source.read_bytes() == data


def test_normalized_reader_wraps_model_errors(tmp_path, normalized_sent):
    source = tmp_path / "normalized.jsonl"
    source.write_text(
        "\n\n" + json.dumps(normalized_sent | {"schema_version": 2}), encoding="utf-8"
    )
    with pytest.raises(EventFileError) as caught:
        list(iter_events(source))
    assert caught.value.line == 3
    assert isinstance(caught.value.__cause__, ValidationError)


def test_raw_reader_wraps_decimal_conversion_error(tmp_path):
    source = tmp_path / "raw.jsonl"
    source.write_text(
        '\n\n{"kind":"Response sent","pid":42,"message":{"code":"+200"}}',
        encoding="utf-8",
    )
    with pytest.raises(EventFileError, match="decimal string") as caught:
        list(iter_raw_events(source))
    assert caught.value.line == 3
    assert type(caught.value.__cause__) is ValueError


@pytest.mark.parametrize("reader", READERS)
def test_readers_reject_each_others_format(tmp_path, raw_sent, normalized_sent, reader):
    source = tmp_path / "wrong-format.jsonl"
    value = normalized_sent if reader is iter_raw_events else raw_sent
    source.write_text("\n\n" + json.dumps(value), encoding="utf-8")
    with pytest.raises(EventFileError) as caught:
        list(reader(source))
    assert caught.value.path == source
    assert caught.value.line == 3
    assert isinstance(caught.value.__cause__, ValidationError)


@pytest.mark.parametrize("reader", READERS)
def test_input_filesystem_errors_are_not_wrapped(tmp_path, reader):
    with pytest.raises(FileNotFoundError):
        list(reader(tmp_path / "absent.jsonl"))


@pytest.mark.parametrize("failure_type", [TypeError, RuntimeError, OSError])
def test_reader_does_not_wrap_programming_or_filesystem_errors(
    write_jsonl, raw_sent, monkeypatch, failure_type
):
    source = write_jsonl(raw_sent)
    failure = failure_type("not malformed input")

    def fail_normalization(record):
        raise failure

    monkeypatch.setattr(event_io, "normalize_record", fail_normalization)
    with pytest.raises(failure_type) as caught:
        list(iter_raw_events(source))
    assert caught.value is failure
