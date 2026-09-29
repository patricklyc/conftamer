"""Subprocess checks for the module and installed console entry points."""

import json
import shutil
import subprocess
import sys
import sysconfig
from pathlib import Path

import pytest

from contexttrack.models import EVENT_ADAPTER, RequestSent


@pytest.fixture(params=["module", "console"])
def cli_command(request):
    if request.param == "module":
        return [sys.executable, "-m", "contexttrack"]
    executable = shutil.which("contexttrack", path=sysconfig.get_path("scripts"))
    assert executable is not None, "contexttrack console script is not installed"
    return [executable]


def _run_cli(
    command: list[str], directory: Path, *args: str | Path
) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [*command, *(str(arg) for arg in args)],
        cwd=directory,
        capture_output=True,
        text=True,
        encoding="utf-8",
        check=False,
        timeout=30,
    )


def _assert_error(result: subprocess.CompletedProcess[str], diagnostic: str) -> None:
    assert result.returncode == 2, result.stderr
    assert result.stdout == ""
    assert diagnostic in result.stderr
    assert "Traceback" not in result.stderr


def test_normalize_cli(tmp_path, raw_sent, cli_command):
    source, output = tmp_path / "raw.jsonl", tmp_path / "normalized.jsonl"
    source.write_text(json.dumps(raw_sent, ensure_ascii=False), encoding="utf-8")
    before = source.read_bytes()
    result = _run_cli(cli_command, tmp_path, "normalize", source, "--output", output)
    assert result.returncode == 0, result.stderr
    assert result.stdout == result.stderr == ""
    event = EVENT_ADAPTER.validate_json(output.read_text(encoding="utf-8"))
    assert isinstance(event, RequestSent)
    assert event.message.path == "/"
    assert event.message.host == "Höst:80"
    assert event.thread_id == 0
    assert output.read_bytes().endswith(b"\n")
    assert "Höst:80" in output.read_text(encoding="utf-8")
    assert json.loads(output.read_text(encoding="utf-8"))["handler"] is None
    assert source.read_bytes() == before


def test_blank_input_publishes_empty_output(tmp_path, cli_command):
    source, output = tmp_path / "raw.jsonl", tmp_path / "normalized.jsonl"
    source.write_text("\n \t\n", encoding="utf-8")
    result = _run_cli(cli_command, tmp_path, "normalize", source, "--output", output)
    assert result.returncode == 0, result.stderr
    assert result.stdout == result.stderr == ""
    assert output.read_bytes() == b""


@pytest.mark.parametrize(
    "args, fragments",
    [
        (["--help"], ["normalize", "schema"]),
        (["normalize", "--help"], ["input", "--output"]),
        (["schema", "--help"], ["schema"]),
    ],
)
def test_help(tmp_path, cli_command, args, fragments):
    result = _run_cli(cli_command, tmp_path, *args)
    assert result.returncode == 0, result.stderr
    assert result.stderr == ""
    assert "usage: contexttrack" in result.stdout
    assert all(fragment in result.stdout for fragment in fragments)
    assert list(tmp_path.iterdir()) == []


@pytest.mark.parametrize(
    "args, diagnostic",
    [
        ([], "required"),
        (["normalize"], "required"),
        (["normalize", "raw.jsonl"], "--output"),
        (["normalize", "--output", "new.jsonl"], "input"),
        (["unknown"], "invalid choice"),
        (
            ["normalize", "raw.jsonl", "--output", "new.jsonl", "--overwrite"],
            "unrecognized arguments",
        ),
        (
            ["normalize", "raw.jsonl", "--output", "new.jsonl", "--skip-bad"],
            "unrecognized arguments",
        ),
        (["schema", "--output", "new.json"], "unrecognized arguments"),
    ],
)
def test_argument_errors(tmp_path, cli_command, args, diagnostic):
    result = _run_cli(cli_command, tmp_path, *args)
    _assert_error(result, diagnostic)
    assert list(tmp_path.iterdir()) == []


@pytest.mark.parametrize(
    "bad_line, diagnostic",
    [
        (b"{", "Expecting"),
        (b"\xff", "utf-8"),
        (b'{"kind":"unknown","pid":42,"message":{}}', "kind"),
        (b'{"kind":"Request received","pid":true,"message":{}}', "pid"),
    ],
)
def test_input_errors_report_physical_line_and_publish_nothing(
    tmp_path, raw_sent, cli_command, bad_line, diagnostic
):
    source, output = tmp_path / "råw.jsonl", tmp_path / "normalized.jsonl"
    before = json.dumps(raw_sent).encode("utf-8") + b"\n\n" + bad_line
    source.write_bytes(before)
    result = _run_cli(cli_command, tmp_path, "normalize", source, "--output", output)
    _assert_error(result, f"{source}:3:")
    assert result.stderr.startswith("contexttrack: ")
    assert diagnostic in result.stderr
    assert source.read_bytes() == before
    assert list(tmp_path.iterdir()) == [source]


@pytest.mark.parametrize("version", [1, 2])
def test_normalized_input_is_not_accepted_as_raw(
    tmp_path, normalized_sent, cli_command, version
):
    source, output = tmp_path / "canonical.jsonl", tmp_path / "normalized.jsonl"
    before = "\n\n" + json.dumps(normalized_sent | {"schema_version": version})
    source.write_text(before, encoding="utf-8")
    result = _run_cli(cli_command, tmp_path, "normalize", source, "--output", output)
    _assert_error(result, f"{source}:3:")
    assert "schema_version" in result.stderr
    assert source.read_text(encoding="utf-8") == before
    assert list(tmp_path.iterdir()) == [source]


def test_missing_input(tmp_path, cli_command):
    source, output = tmp_path / "missing.jsonl", tmp_path / "normalized.jsonl"
    result = _run_cli(cli_command, tmp_path, "normalize", source, "--output", output)
    _assert_error(result, str(source))
    assert result.stderr.startswith("contexttrack: ")
    assert list(tmp_path.iterdir()) == []


def test_existing_output_is_not_changed(tmp_path, raw_sent, cli_command):
    source, output = tmp_path / "raw.jsonl", tmp_path / "normalized.jsonl"
    source.write_text(json.dumps(raw_sent), encoding="utf-8")
    before = source.read_bytes()
    output.write_bytes(b"keep")
    result = _run_cli(cli_command, tmp_path, "normalize", source, "--output", output)
    _assert_error(result, str(output))
    assert source.read_bytes() == before
    assert output.read_bytes() == b"keep"
    assert sorted(path.name for path in tmp_path.iterdir()) == [
        "normalized.jsonl",
        "raw.jsonl",
    ]


def test_output_cannot_replace_input(tmp_path, raw_sent, cli_command):
    source = tmp_path / "raw.jsonl"
    source.write_text(json.dumps(raw_sent), encoding="utf-8")
    before = source.read_bytes()
    result = _run_cli(cli_command, tmp_path, "normalize", source, "--output", source)
    _assert_error(result, str(source))
    assert source.read_bytes() == before
    assert list(tmp_path.iterdir()) == [source]


def test_missing_output_directory_is_not_created(tmp_path, raw_sent, cli_command):
    source, output = tmp_path / "raw.jsonl", tmp_path / "missing/normalized.jsonl"
    source.write_text(json.dumps(raw_sent), encoding="utf-8")
    result = _run_cli(cli_command, tmp_path, "normalize", source, "--output", output)
    _assert_error(result, str(output.parent))
    assert list(tmp_path.iterdir()) == [source]


def test_schema_is_model_generated_sorted_json(tmp_path, cli_command):
    result = _run_cli(cli_command, tmp_path, "schema")
    assert result.returncode == 0, result.stderr
    assert result.stderr == ""
    schema = EVENT_ADAPTER.json_schema()
    assert json.loads(result.stdout) == schema
    assert (
        result.stdout
        == json.dumps(schema, ensure_ascii=False, indent=2, sort_keys=True) + "\n"
    )
    assert list(tmp_path.iterdir()) == []


def test_main_accepts_explicit_argv_and_returns_success(tmp_path, raw_sent, capsys):
    from contexttrack.cli import main

    source, output = tmp_path / "raw.jsonl", tmp_path / "normalized.jsonl"
    source.write_text(json.dumps(raw_sent), encoding="utf-8")
    assert main(["normalize", str(source), "--output", str(output)]) == 0
    streams = capsys.readouterr()
    assert streams.out == streams.err == ""
    event = EVENT_ADAPTER.validate_json(output.read_text(encoding="utf-8"))
    assert event.kind == "send_request"


def test_main_returns_expected_error_code(tmp_path, capsys):
    from contexttrack.cli import main

    source, output = tmp_path / "raw.jsonl", tmp_path / "normalized.jsonl"
    source.write_text("{", encoding="utf-8")
    assert main(["normalize", str(source), "--output", str(output)]) == 2
    streams = capsys.readouterr()
    assert streams.out == ""
    assert f"{source}:1:" in streams.err
    assert "Traceback" not in streams.err
    assert not output.exists()


def test_package_root_exposes_record_and_file_api(tmp_path, raw_sent):
    from contexttrack import (
        EventFileError,
        LocatedEvent,
        iter_events,
        iter_raw_events,
        normalize_file,
        normalize_record,
        write_events,
    )

    source, output = tmp_path / "raw.jsonl", tmp_path / "normalized.jsonl"
    source.write_text(json.dumps(raw_sent), encoding="utf-8")
    event = normalize_record(raw_sent)
    [record] = iter_raw_events(source)
    assert isinstance(record, LocatedEvent)
    assert record.event == event
    assert write_events([event], output) == 1
    assert [record.event for record in iter_events(output)] == [event]
    assert normalize_file(source, tmp_path / "second.jsonl") == 1
    source.write_text("{", encoding="utf-8")
    with pytest.raises(EventFileError, match=rf"{source}:1:"):
        list(iter_raw_events(source))


def test_library_import_does_not_load_cli(tmp_path):
    result = _run_cli(
        [sys.executable],
        tmp_path,
        "-c",
        "import sys; from contexttrack import iter_events, normalize_record; "
        "assert 'contexttrack.cli' not in sys.modules",
    )
    assert result.returncode == 0, result.stderr
    assert result.stdout == result.stderr == ""
