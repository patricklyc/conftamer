"""Public exports and cold imports, independent of CLI and capture tooling."""

import os
import subprocess
import sys

import pytest

import contexttrack
from contexttrack import io, models, normalize


@pytest.mark.parametrize(
    "module, names",
    [
        (
            models,
            [
                "ContextInfo",
                "RequestFields",
                "RequestMessage",
                "SentRequestMessage",
                "RoutedRequestMessage",
                "ResponseMessage",
                "RequestSent",
                "RequestReceived",
                "RequestRouted",
                "ResponseSent",
                "ResponseReceived",
                "Event",
                "EVENT_ADAPTER",
            ],
        ),
        (
            io,
            [
                "EventFileError",
                "LocatedEvent",
                "iter_events",
                "iter_raw_events",
                "normalize_file",
                "write_events",
            ],
        ),
        (normalize, ["normalize_record"]),
    ],
    ids=["models", "io", "normalize"],
)
def test_public_exports_are_module_objects(module, names):
    for name in names:
        assert getattr(contexttrack, name) is getattr(module, name)


def test_import_is_quiet_and_independent_of_cli_capture_and_go(tmp_path):
    capture = tmp_path / "must-not-exist.jsonl"
    result = subprocess.run(
        [
            sys.executable,
            "-c",
            (
                "import sys; "
                "from contexttrack import EVENT_ADAPTER, RequestSent, iter_events, "
                "normalize_record; assert 'contexttrack.cli' not in sys.modules"
            ),
        ],
        cwd=tmp_path,
        env=os.environ
        | {
            "CONFTAMER_EVENTS": str(capture),
            "GOROOT": str(tmp_path / "no-go"),
            "PATH": "",
        },
        capture_output=True,
        text=True,
        encoding="utf-8",
        check=False,
        timeout=30,
    )
    assert result.returncode == 0, result.stderr
    assert result.stdout == result.stderr == ""
    assert not capture.exists()
