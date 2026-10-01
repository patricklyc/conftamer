"""Synthetic raw captures and normalized event fixtures."""

import json
from pathlib import Path

import pytest

from contexttrack.models import EVENT_ADAPTER, RequestSent
from contexttrack.normalize import normalize_record


@pytest.fixture(params=["raw", "normalized"])
def boundary(request, raw_sent, normalized_sent):
    if request.param == "raw":
        return normalize_record, raw_sent
    return EVENT_ADAPTER.validate_python, normalized_sent


@pytest.fixture
def write_jsonl(tmp_path):
    def write(*records: object, name: str = "input.jsonl") -> Path:
        path = tmp_path / name
        path.write_text(
            "".join(json.dumps(value, ensure_ascii=False) + "\n" for value in records),
            encoding="utf-8",
        )
        return path

    return write


@pytest.fixture
def event(normalized_sent):
    value = EVENT_ADAPTER.validate_python(normalized_sent)
    assert isinstance(value, RequestSent)
    return value


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
