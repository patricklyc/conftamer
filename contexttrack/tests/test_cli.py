"""Direct CLI contracts and four smokes for each installed entry point."""

import errno
import json
import shutil
import subprocess
import sys
import sysconfig
from pathlib import Path

import pytest

from contexttrack import cli
from contexttrack.io import EventFileError
from contexttrack.models import EVENT_ADAPTER


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


@pytest.mark.parametrize(
    "args, fragments",
    [
        (["--help"], ["normalize", "schema"]),
        (["normalize", "--help"], ["input", "--output"]),
        (["schema", "--help"], ["schema"]),
    ],
)
def test_main_help(tmp_path, monkeypatch, args, fragments, capsys):
    monkeypatch.chdir(tmp_path)
    with pytest.raises(SystemExit) as caught:
        cli.main(args)
    assert caught.value.code == 0
    stdout, stderr = capsys.readouterr()
    assert stderr == ""
    assert "usage: contexttrack" in stdout
    assert all(fragment in stdout for fragment in fragments)
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
def test_main_argument_errors(tmp_path, monkeypatch, args, diagnostic, capsys):
    monkeypatch.chdir(tmp_path)
    with pytest.raises(SystemExit) as caught:
        cli.main(args)
    assert caught.value.code == 2
    stdout, stderr = capsys.readouterr()
    assert stdout == ""
    assert diagnostic in stderr
    assert "Traceback" not in stderr
    assert list(tmp_path.iterdir()) == []


def test_normalize_delegates_paths(tmp_path, monkeypatch, capsys):
    calls = []

    def record_call(source, output):
        calls.append((source, output))
        return 1

    monkeypatch.setattr(cli, "normalize_file", record_call)
    source, output = tmp_path / "raw.jsonl", tmp_path / "new.jsonl"
    assert cli.main(["normalize", str(source), "--output", str(output)]) == 0
    assert calls == [(source, output)]
    assert capsys.readouterr() == ("", "")


@pytest.mark.parametrize(
    "error",
    [
        EventFileError(Path("råw.jsonl"), 3, "malformed JSON"),
        OSError(errno.ENOSPC, "cannot publish output", "nöw.jsonl"),
    ],
    ids=["event-file", "os"],
)
def test_main_reports_expected_errors(monkeypatch, capsys, error):
    def fail_normalization(source, output):
        raise error

    monkeypatch.setattr(cli, "normalize_file", fail_normalization)
    assert cli.main(["normalize", "raw.jsonl", "--output", "new.jsonl"]) == 2
    stdout, stderr = capsys.readouterr()
    assert stdout == ""
    assert stderr == f"contexttrack: {error}\n"
    assert "Traceback" not in stderr


def test_main_schema_is_model_generated_sorted_json(tmp_path, monkeypatch, capsys):
    monkeypatch.chdir(tmp_path)
    assert cli.main(["schema"]) == 0
    stdout, stderr = capsys.readouterr()
    assert stderr == ""
    schema = EVENT_ADAPTER.json_schema()
    assert json.loads(stdout) == schema
    assert (
        stdout
        == json.dumps(schema, ensure_ascii=False, indent=2, sort_keys=True) + "\n"
    )
    assert list(tmp_path.iterdir()) == []


def test_entry_point_help(tmp_path, cli_command):
    result = _run_cli(cli_command, tmp_path, "--help")
    assert result.returncode == 0, result.stderr
    assert result.stderr == ""
    assert "usage: contexttrack" in result.stdout
    assert "normalize" in result.stdout and "schema" in result.stdout
    assert list(tmp_path.iterdir()) == []


def test_entry_point_schema(tmp_path, cli_command):
    result = _run_cli(cli_command, tmp_path, "schema")
    assert result.returncode == 0, result.stderr
    assert result.stderr == ""
    assert json.loads(result.stdout) == EVENT_ADAPTER.json_schema()
    assert list(tmp_path.iterdir()) == []


def test_entry_point_normalize(tmp_path, raw_sent, write_jsonl, cli_command):
    source = write_jsonl(raw_sent)
    output = tmp_path / "normalized.jsonl"
    result = _run_cli(cli_command, tmp_path, "normalize", source, "--output", output)
    assert result.returncode == 0, result.stderr
    assert result.stdout == result.stderr == ""
    assert json.loads(output.read_text(encoding="utf-8"))["kind"] == "send_request"


def test_entry_point_malformed_input(tmp_path, raw_sent, write_jsonl, cli_command):
    source = write_jsonl(raw_sent, name="råw.jsonl")
    source.write_text(source.read_text(encoding="utf-8") + "\n{", encoding="utf-8")
    output = tmp_path / "normalized.jsonl"
    result = _run_cli(cli_command, tmp_path, "normalize", source, "--output", output)
    assert result.returncode == 2, result.stderr
    assert result.stdout == ""
    assert result.stderr.startswith(f"contexttrack: {source}:3:")
    assert "Expecting" in result.stderr
    assert "Traceback" not in result.stderr
    assert not output.exists()
