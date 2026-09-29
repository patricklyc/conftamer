"""Synthetic raw captures and normalized event fixtures."""

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


@pytest.fixture
def raw_sent():
    return {
        "kind": "Request sent",
        "pid": 42,
        "thread_id": 0,
        "context": {"context_id": "id:7"},
        "api_id": " API\n",
        "message": {
            "req.Method": "gEt",
            "req.URL.Host": "Höst:80",
            "req.URL.Path": "",
            "req.URL.RawQuery": "",
        },
        "request_id": {"method": "gEt", "host": "Höst:80", "path": ""},
    }
