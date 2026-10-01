"""Check public schema generation without a checked-in snapshot."""

import json
import subprocess
import sys

from contexttrack.models import EVENT_ADAPTER


def test_cli_schema_agrees_with_models():
    result = subprocess.run(
        [sys.executable, "-m", "contexttrack", "schema"],
        capture_output=True,
        text=True,
        encoding="utf-8",
        check=True,
        timeout=30,
    )
    assert json.loads(result.stdout) == EVENT_ADAPTER.json_schema()
