"""Streaming serialization and atomic, no-overwrite normalized publication."""

import errno
import json
from contextlib import contextmanager
from pathlib import Path

import pytest
from pydantic import ValidationError

from contexttrack import io as event_io
from contexttrack.io import EventFileError, normalize_file, write_events
from contexttrack.models import ContextInfo, RequestSent, SentRequestMessage


@pytest.mark.parametrize(
    "bad_line, cause_type",
    [
        (b"{", json.JSONDecodeError),
        (b"\xff", UnicodeDecodeError),
        (b'{"kind":"Request routed","pid":true,"message":{}}', ValidationError),
    ],
)
def test_late_record_failure_does_not_publish_prefix(
    tmp_path, raw_sent, bad_line, cause_type
):
    source, output = tmp_path / "raw.jsonl", tmp_path / "normalized.jsonl"
    data = json.dumps(raw_sent).encode("utf-8") + b"\n\n" + bad_line
    source.write_bytes(data)
    with pytest.raises(EventFileError) as caught:
        normalize_file(source, output)
    assert caught.value.path == source
    assert caught.value.line == 3
    assert str(caught.value).startswith(f"{source}:3:")
    assert isinstance(caught.value.__cause__, cause_type)
    assert source.read_bytes() == data
    assert not output.exists()
    assert sorted(path.name for path in tmp_path.iterdir()) == [source.name]


@pytest.mark.parametrize("text", ["", "\n \t\r\n\n"])
def test_empty_normalization_publishes_empty_file(tmp_path, text):
    source, output = tmp_path / "raw.jsonl", tmp_path / "normalized.jsonl"
    source.write_text(text, encoding="utf-8")
    assert normalize_file(str(source), str(output)) == 0
    assert output.read_bytes() == b""
    assert sorted(path.name for path in tmp_path.iterdir()) == [
        output.name,
        source.name,
    ]


def test_never_overwrite_existing_file(tmp_path, write_jsonl, raw_sent):
    source = write_jsonl(raw_sent)
    output = tmp_path / "normalized.jsonl"
    output.write_bytes(b"keep")
    with pytest.raises(FileExistsError):
        normalize_file(source, output)
    assert output.read_bytes() == b"keep"


def test_output_cannot_be_input(write_jsonl, raw_sent):
    source = write_jsonl(raw_sent)
    before = source.read_bytes()
    with pytest.raises(FileExistsError):
        normalize_file(source, source)
    assert source.read_bytes() == before


@pytest.mark.parametrize("dangling", [False, True])
def test_existing_output_symlink_is_never_followed_or_replaced(
    tmp_path, write_jsonl, raw_sent, dangling
):
    source = write_jsonl(raw_sent)
    output, target = tmp_path / "normalized.jsonl", tmp_path / "target"
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


def test_missing_output_directory_is_not_created(tmp_path, event):
    output = tmp_path / "absent" / "normalized.jsonl"
    with pytest.raises(FileNotFoundError):
        write_events([event], output)
    assert list(tmp_path.iterdir()) == []


def test_missing_input_cleans_up_writer_temporary_file(tmp_path):
    with pytest.raises(FileNotFoundError):
        normalize_file(tmp_path / "absent.jsonl", tmp_path / "normalized.jsonl")
    assert list(tmp_path.iterdir()) == []


@pytest.mark.parametrize("failure_stage", ["write", "close"])
def test_write_or_close_failure_never_publishes_output(
    tmp_path, event, monkeypatch, failure_stage
):
    output = tmp_path / "normalized.jsonl"
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
    tmp_path, event, monkeypatch, destination_kind
):
    output = tmp_path / "normalized.jsonl"
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
    assert sorted(path.name for path in tmp_path.iterdir()) == [output.name]


def test_unsupported_hard_links_do_not_fall_back_to_overwriting(
    tmp_path, event, monkeypatch
):
    output = tmp_path / "normalized.jsonl"
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
    tmp_path, event, invalid_part
):
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


def test_writer_uses_canonical_serialization(tmp_path, event):
    output = tmp_path / "normalized.jsonl"
    assert write_events([event], output) == 1
    text = output.read_text(encoding="utf-8")
    assert json.loads(text) == event.model_dump(mode="json")
    assert "Höst:80" in text
    assert text.endswith("\n") and len(text.splitlines()) == 1
    assert sorted(path.name for path in tmp_path.iterdir()) == [output.name]


def test_failing_event_iterable_leaves_no_output(tmp_path, event):
    failure = RuntimeError("iterator failed")

    def events():
        yield event
        raise failure

    with pytest.raises(RuntimeError) as caught:
        write_events(events(), tmp_path / "normalized.jsonl")
    assert caught.value is failure
    assert list(tmp_path.iterdir()) == []
