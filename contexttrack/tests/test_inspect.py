import json
import re
import subprocess
import sys
from pathlib import Path

import contexttrack
from contexttrack.capture import Capture, Occurrence, RecordedEvent, read_capture
from contexttrack.inspect import main

ROOT = Path(__file__).parents[1]
FIXTURE = ROOT / "testdata/v2-chain.jsonl"
PROCESS_A = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"


def run_cli(monkeypatch, capsys, *arguments: str) -> tuple[str, str]:
    monkeypatch.setattr(sys, "argv", ["contexttrack", *arguments])
    main()
    captured = capsys.readouterr()
    return captured.out, captured.err


def test_groups_retains_occurrences_and_prints_scoped_ids(monkeypatch, capsys):
    stdout, stderr = run_cli(
        monkeypatch,
        capsys,
        "groups",
        str(FIXTURE),
    )

    scope = f"synthetic-chain/{PROCESS_A}"
    assert f"Context {scope}/7 (4 occurrences)" in stdout
    assert stdout.count("  seq=") == 4
    assert f"seq=1 exchange={scope}/1" in stdout
    assert f"seq=3 exchange={scope}/2" in stdout
    assert f"seq=4 exchange={scope}/2" in stdout
    assert f"seq=5 exchange={scope}/1" in stdout
    assert "4 occurrences in 1 known context; 0 unknown-context occurrences" in stdout
    assert stderr == ""


def test_graph_text_keeps_nodes_and_describes_possible_influence(
    monkeypatch, capsys
):
    stdout, stderr = run_cli(
        monkeypatch,
        capsys,
        "graph",
        str(FIXTURE),
        "--format",
        "text",
    )

    assert stdout.startswith("Possible-influence graph (shared context)\n")
    assert "Nodes (4):" in stdout
    assert "Edges (4):" in stdout
    assert len(re.findall(r"^  n\d+ -> n\d+$", stdout, re.MULTILINE)) == 4
    assert stderr == ""


def test_graph_text_retains_an_isolated_message(tmp_path, monkeypatch, capsys):
    path = tmp_path / "isolated.jsonl"
    path.write_bytes(FIXTURE.read_bytes().splitlines()[0] + b"\n")

    stdout, _ = run_cli(
        monkeypatch,
        capsys,
        "graph",
        str(path),
        "--format",
        "text",
    )

    assert "Nodes (1):" in stdout
    assert "Edges (0):" in stdout
    assert '"kind":"receive_request"' in stdout


def test_graph_dot_escapes_strings_and_reports_summary_to_stderr(
    tmp_path, monkeypatch, capsys
):
    record = json.loads(FIXTURE.read_text().splitlines()[0])
    record["request"]["method"] = 'G"ET'
    record["request"]["path"] = '/quote"\\雪\nline'
    path = tmp_path / "special.jsonl"
    path.write_text(json.dumps(record, ensure_ascii=False) + "\n")

    stdout, stderr = run_cli(
        monkeypatch,
        capsys,
        "graph",
        str(path),
        "--format",
        "dot",
    )

    assert stdout.startswith("digraph possible_influence {\n")
    assert stdout.endswith("}\n")
    label_line = next(line for line in stdout.splitlines() if "[label=" in line)
    assert r'method: G\"ET' in label_line
    assert r'path: /quote\"\\雪\nline' in label_line
    assert "occurrences=1 nodes=1 possible_influence_edges=0" in stderr


def test_empty_dot_graph_is_valid(monkeypatch, capsys, tmp_path):
    stdout, stderr = run_cli(
        monkeypatch,
        capsys,
        "graph",
        str(tmp_path),
        "--format",
        "dot",
    )

    assert stdout.startswith("digraph possible_influence {\n")
    assert stdout.endswith("}\n")
    assert "occurrences=0 nodes=0 possible_influence_edges=0" in stderr


def test_package_exports_capture_api():
    assert contexttrack.Capture is Capture
    assert contexttrack.Occurrence is Occurrence
    assert contexttrack.RecordedEvent is RecordedEvent
    assert contexttrack.read_capture is read_capture
    assert callable(contexttrack.shared_context_pairs)


def test_project_console_script_dispatches_graph():
    command = Path(sys.executable).with_name("contexttrack")

    result = subprocess.run(
        [command, "graph", FIXTURE, "--format", "text"],
        check=False,
        capture_output=True,
        text=True,
    )

    assert result.returncode == 0, result.stderr
    assert "Possible-influence graph (shared context)" in result.stdout
    assert "Edges (4):" in result.stdout


def test_analysis_scripts_are_thin_cli_wrappers():
    cases = [
        (ROOT / "analysis/group_by_context.py", [str(FIXTURE)], "Context "),
        (
            ROOT / "analysis/message_graph.py",
            [str(FIXTURE), "--format", "text"],
            "Possible-influence graph (shared context)",
        ),
    ]

    for script, arguments, expected in cases:
        result = subprocess.run(
            [sys.executable, script, *arguments],
            check=False,
            capture_output=True,
            text=True,
        )
        assert result.returncode == 0, result.stderr
        assert expected in result.stdout


def test_graph_rejects_removed_legacy_mode():
    command = Path(sys.executable).with_name("contexttrack")

    result = subprocess.run(
        [command, "graph", FIXTURE, "--recv-sent"],
        check=False,
        capture_output=True,
        text=True,
    )

    assert result.returncode == 2
    assert "unrecognized arguments: --recv-sent" in result.stderr
