"""Strict JSONL readers and atomic, no-overwrite normalized output."""

import errno
import json
from contextlib import contextmanager
from dataclasses import FrozenInstanceError
from pathlib import Path

import pytest
from pydantic import ValidationError

from contexttrack import io as event_io
from contexttrack.io import (
    EventFileError,
    iter_events,
    iter_raw_events,
    normalize_file,
    write_events,
)
from contexttrack.models import (
    EVENT_ADAPTER,
    ContextInfo,
    RequestSent,
    SentRequestMessage,
)

READERS = (iter_raw_events, iter_events)


def test_raw_reader_normalizes_in_memory(tmp_path, raw_sent):
    source = tmp_path / "raw.jsonl"
    source.write_text(
        "\n" + json.dumps(raw_sent, ensure_ascii=False) + "\n", encoding="utf-8"
    )
    before = source.read_bytes()
    [record] = iter_raw_events(source)
    assert record.event.kind == "send_request"
    assert record.event.message.path == "/"
    assert record.location == f"{source}:2"
    assert source.read_bytes() == before
    assert sorted(path.name for path in tmp_path.iterdir()) == ["raw.jsonl"]


def test_raw_record_error_has_raw_physical_line(tmp_path, raw_sent):
    source = tmp_path / "raw.jsonl"
    bad = raw_sent | {"kind": "unknown"}
    source.write_text(json.dumps(raw_sent) + "\n\n" + json.dumps(bad), encoding="utf-8")
    records = iter_raw_events(source)
    assert next(records).line == 1
    with pytest.raises(EventFileError, match=rf"{source}:3:"):
        next(records)


def test_count_order_duplicates_and_physical_lines(tmp_path, raw_sent):
    source, output = tmp_path / "raw.jsonl", tmp_path / "normalized.jsonl"
    raw_line = json.dumps(raw_sent, ensure_ascii=False)
    source.write_text("\n" + raw_line + "\n\n" + raw_line, encoding="utf-8")
    before = source.read_bytes()
    assert normalize_file(source, output) == 2
    records = list(iter_events(output))
    assert len(records) == 2
    assert records[0].event == records[1].event
    assert [record.line for record in records] == [1, 2]
    assert records[0].location == f"{output}:1"
    assert source.read_bytes() == before
    assert output.read_bytes().endswith(b"\n")


def test_bad_later_record_does_not_publish_prefix(tmp_path, raw_sent):
    source, output = tmp_path / "raw.jsonl", tmp_path / "normalized.jsonl"
    source.write_text(json.dumps(raw_sent) + "\n\n{\n", encoding="utf-8")
    with pytest.raises(EventFileError, match=rf"{source}:3:"):
        normalize_file(source, output)
    assert not output.exists()
    assert sorted(path.name for path in tmp_path.iterdir()) == ["raw.jsonl"]


def test_never_overwrite_existing_file(tmp_path, raw_sent):
    source, output = tmp_path / "raw.jsonl", tmp_path / "normalized.jsonl"
    source.write_text(json.dumps(raw_sent), encoding="utf-8")
    output.write_bytes(b"keep")
    with pytest.raises(FileExistsError):
        normalize_file(source, output)
    assert output.read_bytes() == b"keep"


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


@pytest.mark.parametrize("text", ["", "\n \t\r\n\n"])
def test_empty_normalization_publishes_empty_file(tmp_path, text):
    source, output = tmp_path / "raw.jsonl", tmp_path / "normalized.jsonl"
    source.write_text(text, encoding="utf-8")
    assert normalize_file(str(source), str(output)) == 0
    assert output.read_bytes() == b""
    assert sorted(path.name for path in tmp_path.iterdir()) == [
        "normalized.jsonl",
        "raw.jsonl",
    ]


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


@pytest.mark.parametrize(
    "changes",
    [
        {"schema_version": 2},
        {"schema_version": True},
        {"pid": "42"},
        {"context": None},
        {"message": {"path": ""}},
        {"message": {"host": False}},
        {"extra": None},
    ],
)
def test_normalized_reader_wraps_model_errors(tmp_path, normalized_sent, changes):
    source = tmp_path / "normalized.jsonl"
    source.write_text("\n\n" + json.dumps(normalized_sent | changes), encoding="utf-8")
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


@pytest.mark.parametrize(
    "bad_line, cause_type",
    [
        (b"\xff", UnicodeDecodeError),
        (b'{"kind":"Request routed","pid":true,"message":{}}', ValidationError),
    ],
)
def test_late_decode_or_model_error_removes_temporary_output(
    tmp_path, raw_sent, bad_line, cause_type
):
    source, output = tmp_path / "raw.jsonl", tmp_path / "normalized.jsonl"
    data = json.dumps(raw_sent).encode("utf-8") + b"\n\n" + bad_line
    source.write_bytes(data)
    with pytest.raises(EventFileError) as caught:
        normalize_file(source, output)
    assert caught.value.line == 3
    assert isinstance(caught.value.__cause__, cause_type)
    assert source.read_bytes() == data
    assert sorted(path.name for path in tmp_path.iterdir()) == ["raw.jsonl"]


def test_output_cannot_be_input(tmp_path, raw_sent):
    source = tmp_path / "raw.jsonl"
    source.write_text(json.dumps(raw_sent), encoding="utf-8")
    before = source.read_bytes()
    with pytest.raises(FileExistsError):
        normalize_file(source, source)
    assert source.read_bytes() == before


@pytest.mark.parametrize("dangling", [False, True])
def test_existing_output_symlink_is_never_followed_or_replaced(
    tmp_path, raw_sent, dangling
):
    source, output = tmp_path / "raw.jsonl", tmp_path / "normalized.jsonl"
    target = tmp_path / "target"
    source.write_text(json.dumps(raw_sent), encoding="utf-8")
    if not dangling:
        target.write_bytes(b"keep")
    output.symlink_to(target)
    with pytest.raises(FileExistsError):
        normalize_file(source, output)
    assert output.is_symlink()
    assert output.readlink() == target
    if dangling:
        assert not target.exists()
    else:
        assert target.read_bytes() == b"keep"
    assert not list(tmp_path.glob(".contexttrack-*.tmp"))


def test_missing_output_directory_is_not_created(tmp_path, normalized_sent):
    output = tmp_path / "absent" / "normalized.jsonl"
    event = EVENT_ADAPTER.validate_python(normalized_sent)
    with pytest.raises(FileNotFoundError):
        write_events([event], output)
    assert list(tmp_path.iterdir()) == []


@pytest.mark.parametrize("reader", READERS)
def test_input_filesystem_errors_are_not_wrapped(tmp_path, reader):
    with pytest.raises(FileNotFoundError):
        list(reader(tmp_path / "absent.jsonl"))


def test_missing_input_cleans_up_writer_temporary_file(tmp_path):
    with pytest.raises(FileNotFoundError):
        normalize_file(tmp_path / "absent.jsonl", tmp_path / "normalized.jsonl")
    assert list(tmp_path.iterdir()) == []


@pytest.mark.parametrize("failure_stage", ["write", "close"])
def test_write_or_close_failure_never_publishes_output(
    tmp_path, normalized_sent, monkeypatch, failure_stage
):
    output = tmp_path / "normalized.jsonl"
    event = EVENT_ADAPTER.validate_python(normalized_sent)
    failure = OSError(errno.ENOSPC, "simulated output failure")
    real_temporary_file = event_io.tempfile.NamedTemporaryFile

    @contextmanager
    def failing_temporary_file(**kwargs):
        with real_temporary_file(**kwargs) as target:
            if failure_stage == "write":

                def partial_write(text):
                    target.file.write(text[:10])
                    raise failure

                monkeypatch.setattr(target, "write", partial_write)
            yield target
        if failure_stage == "close":
            raise failure

    monkeypatch.setattr(event_io.tempfile, "NamedTemporaryFile", failing_temporary_file)
    with pytest.raises(OSError) as caught:
        write_events([event], output)
    assert caught.value is failure
    assert list(tmp_path.iterdir()) == []


@pytest.mark.parametrize("destination_kind", ["file", "symlink"])
def test_publication_race_cannot_overwrite_another_writer(
    tmp_path, normalized_sent, monkeypatch, destination_kind
):
    output = tmp_path / "normalized.jsonl"
    event = EVENT_ADAPTER.validate_python(normalized_sent)
    real_link = event_io.os.link
    missing_target = tmp_path / "missing-target"

    def racing_link(source, destination):
        destination = Path(destination)
        if destination_kind == "file":
            destination.write_bytes(b"another writer")
        else:
            destination.symlink_to(missing_target)
        real_link(source, destination)

    monkeypatch.setattr(event_io.os, "link", racing_link)
    with pytest.raises(FileExistsError):
        write_events([event], output)
    if destination_kind == "file":
        assert output.read_bytes() == b"another writer"
    else:
        assert output.is_symlink()
        assert output.readlink() == missing_target
        assert not missing_target.exists()
    assert sorted(path.name for path in tmp_path.iterdir()) == ["normalized.jsonl"]


def test_unsupported_hard_links_do_not_fall_back_to_overwriting(
    tmp_path, normalized_sent, monkeypatch
):
    output = tmp_path / "normalized.jsonl"
    event = EVENT_ADAPTER.validate_python(normalized_sent)
    failure = OSError(errno.EOPNOTSUPP, "hard links unavailable")

    def unsupported_link(source, destination):
        raise failure

    monkeypatch.setattr(event_io.os, "link", unsupported_link)
    with pytest.raises(OSError) as caught:
        write_events([event], output)
    assert caught.value is failure
    assert list(tmp_path.iterdir()) == []


@pytest.mark.parametrize("invalid_part", ["envelope", "context", "message"])
def test_writer_revalidates_constructed_models_before_reading_next_value(
    tmp_path, normalized_sent, invalid_part
):
    event = EVENT_ADAPTER.validate_python(normalized_sent)
    assert isinstance(event, RequestSent)
    fields = {
        "schema_version": 1,
        "kind": "send_request",
        "pid": 42,
        "context": event.context,
        "message": event.message,
    }
    if invalid_part == "envelope":
        fields["pid"] = True
    elif invalid_part == "context":
        fields["context"] = ContextInfo.model_construct(context_id=7)
    else:
        fields["message"] = SentRequestMessage.model_construct(path="")
    invalid = RequestSent.model_construct(_fields_set=None, **fields)

    def events():
        yield event
        yield invalid
        pytest.fail("writer consumed past the invalid event instead of streaming")

    with pytest.raises(ValidationError):
        write_events(events(), tmp_path / "normalized.jsonl")
    assert list(tmp_path.iterdir()) == []


def test_writer_serializes_canonical_models_with_unicode_and_explicit_nulls(
    tmp_path, normalized_sent
):
    output = tmp_path / "normalized.jsonl"
    event = EVENT_ADAPTER.validate_python(normalized_sent)
    assert write_events([event], str(output)) == 1
    text = output.read_text(encoding="utf-8")
    assert text.endswith("\n")
    assert len(text.splitlines()) == 1
    assert "Höst:80" in text
    assert json.loads(text) == {
        "schema_version": 1,
        "kind": "send_request",
        "pid": 42,
        "context": {
            "context_id": "id:7",
            "source": None,
            "type": None,
            "error": None,
        },
        "api_id": None,
        "handler": None,
        "goroutine_id": None,
        "thread_id": None,
        "file": None,
        "line": None,
        "message": {
            "method": "gEt",
            "path": "/",
            "raw_query": "",
            "host": "Höst:80",
        },
    }
    assert sorted(path.name for path in tmp_path.iterdir()) == ["normalized.jsonl"]


@pytest.mark.parametrize("failure_type", [TypeError, RuntimeError, OSError])
def test_reader_does_not_wrap_programming_or_filesystem_errors(
    tmp_path, raw_sent, monkeypatch, failure_type
):
    source = tmp_path / "raw.jsonl"
    source.write_text(json.dumps(raw_sent), encoding="utf-8")
    failure = failure_type("not malformed input")

    def fail_normalization(record):
        raise failure

    monkeypatch.setattr(event_io, "normalize_record", fail_normalization)
    with pytest.raises(failure_type) as caught:
        list(iter_raw_events(source))
    assert caught.value is failure


def test_failing_event_iterable_leaves_no_output(tmp_path, normalized_sent):
    event = EVENT_ADAPTER.validate_python(normalized_sent)
    failure = RuntimeError("iterator failed")

    def events():
        yield event
        raise failure

    with pytest.raises(RuntimeError) as caught:
        write_events(events(), tmp_path / "normalized.jsonl")
    assert caught.value is failure
    assert list(tmp_path.iterdir()) == []


def test_multikind_stream_retains_order_incomplete_records_and_repeated_hooks(
    tmp_path, raw_sent
):
    source, output = tmp_path / "raw.jsonl", tmp_path / "normalized.jsonl"
    received = {
        "kind": "Request received",
        "pid": 42,
        "context": {"context_id": "id:7"},
        "api_id": "server-api",
        "handler": "serve",
        "message": {"req.Method": "gEt", "req.URL.Path": ""},
    }
    response = {
        "kind": "Response received",
        "pid": 42,
        "context": {"context_id": "id:7"},
        "message": {"req.Method": "gEt", "req.URL.Path": "", "resp.StatusCode": "0200"},
    }
    raw_records = [
        raw_sent,
        received,
        {"kind": "Request routed", "pid": 42, "message": {}},
        {
            "kind": "Request routed",
            "pid": 42,
            "context": {"context_id": "id:7"},
            "message": {
                "req.Method": "gEt",
                "req.URL.Path": "",
                "pattern": "GET /{id}",
            },
        },
        {
            "kind": "Response sent",
            "pid": 42,
            "context": {"context_id": "id:7"},
            "message": {"req.Method": "gEt", "req.URL.Path": "", "code": "103"},
        },
        response,
        response,
    ]
    source.write_text(
        "\n"
        + "\n\n".join(json.dumps(value, ensure_ascii=False) for value in raw_records),
        encoding="utf-8",
    )
    before = source.read_bytes()
    direct = list(iter_raw_events(source))
    assert normalize_file(source, output) == 7
    stored = list(iter_events(output))
    events = [record.event for record in stored]
    assert events == [record.event for record in direct]
    assert [event.kind for event in events] == [
        "send_request",
        "receive_request",
        "request_routed",
        "request_routed",
        "send_response",
        "receive_response",
        "receive_response",
    ]
    assert [record.line for record in direct] == [2, 4, 6, 8, 10, 12, 14]
    assert [record.line for record in stored] == [1, 2, 3, 4, 5, 6, 7]
    assert all(record.path == source for record in direct)
    assert all(record.path == output for record in stored)
    assert events[0].api_id == " API\n"
    assert events[1].api_id == "server-api"
    assert events[1].handler == "serve"
    assert events[2].message.model_dump() == {
        "method": None,
        "path": None,
        "pattern": None,
    }
    assert events[2].context_key is None
    assert events[3].message.model_dump() == {
        "method": "gEt",
        "path": "/",
        "pattern": "GET /{id}",
    }
    assert events[4].message.model_dump() == {
        "method": "gEt",
        "path": "/",
        "status_code": 103,
    }
    assert events[5].message.model_dump() == {
        "method": "gEt",
        "path": "/",
        "status_code": 200,
    }
    assert all(event.api_id is None and event.handler is None for event in events[2:])
    assert events[5] == events[6]
    assert source.read_bytes() == before
