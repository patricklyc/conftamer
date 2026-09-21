import json
import re
import subprocess
import sys
from pathlib import Path

from contexttrack.inspect import main

ROOT = Path(__file__).parents[1]
FIXTURE = ROOT / "testdata/v4-chain.jsonl"


def run_cli(monkeypatch, capsys, *arguments: str) -> tuple[str, str]:
    monkeypatch.setattr(sys, "argv", ["contexttrack", *arguments])
    main()
    captured = capsys.readouterr()
    return captured.out, captured.err


def test_v4_cli_is_deterministic_and_shows_witnesses(monkeypatch, capsys):
    stdout, stderr = run_cli(monkeypatch, capsys, str(FIXTURE))

    assert stdout.startswith("Influence graph (recorded sources)\n")
    assert "Nodes (4):" in stdout
    assert "Edges (3):" in stdout
    assert re.findall(r"^  n\d+ -> n\d+ .+$", stdout, re.MULTILINE) == [
        f"  n0 -> n1 declared source={FIXTURE}:1 target={FIXTURE}:2",
        f"  n0 -> n3 request/reply source={FIXTURE}:1 target={FIXTURE}:4",
        f"  n2 -> n3 declared source={FIXTURE}:3 target={FIXTURE}:4",
    ]
    assert "occurrences=4 nodes=4 edges=3 sends_without_sources=0" in stdout
    assert stderr == ""


def test_cli_retains_unannotated_send_without_claiming_independence(
    tmp_path, monkeypatch, capsys
):
    record = json.loads(FIXTURE.read_text().splitlines()[1])
    record.update(seq=1, sources=[])
    path = tmp_path / "isolated.jsonl"
    path.write_text(json.dumps(record) + "\n")

    stdout, stderr = run_cli(monkeypatch, capsys, str(path))

    assert "Nodes (1):" in stdout
    assert "Edges (0):" in stdout
    assert '"kind":"send_request"' in stdout
    assert "occurrences=1 nodes=1 edges=0 sends_without_sources=1" in stdout
    assert "independen" not in stdout.lower()
    assert stderr == ""


def test_installed_console_command_uses_the_sole_interface():
    command = Path(sys.executable).with_name("contexttrack")

    result = subprocess.run(
        [command, FIXTURE],
        check=False,
        capture_output=True,
        text=True,
    )

    assert result.returncode == 0, result.stderr
    assert "Nodes (4):" in result.stdout
    assert "Edges (3):" in result.stdout
    assert result.stderr == ""


def test_removed_format_flag_is_rejected():
    command = Path(sys.executable).with_name("contexttrack")
    result = subprocess.run(
        [command, FIXTURE, "--format", "dot"],
        check=False,
        capture_output=True,
        text=True,
    )

    assert result.returncode == 2
    assert "unrecognized arguments" in result.stderr
