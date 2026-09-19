from importlib import import_module
from pathlib import Path

import pytest

ROOT = Path(__file__).parents[1]


def test_full_pattern_error_identifies_invalid_pattern(monkeypatch):
    monkeypatch.syspath_prepend(str(ROOT / "analysis"))
    full_pattern = import_module("message_graph")._full_pattern

    with pytest.raises(AssertionError, match="pattern without '/': invalid"):
        full_pattern("/prefix/resource", "/resource", "invalid")
