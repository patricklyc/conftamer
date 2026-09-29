"""Synthetic fixtures for the normalized event contract."""

import pytest


@pytest.fixture
def normalized_sent():
    return {
        "schema_version": 1,
        "kind": "send_request",
        "pid": 42,
        "context": {"context_id": "id:7"},
        "message": {
            "method": "gEt",
            "host": "Höst:80",
            "path": "/",
            "raw_query": "",
        },
    }
