"""One multikind raw-to-normalized stream, retaining evidence without association."""

import json

from contexttrack.io import iter_events, iter_raw_events, normalize_file


def test_multikind_stream_retains_order_incomplete_records_and_repeated_hooks(
    tmp_path, raw_sent
):
    source, output = tmp_path / "raw.jsonl", tmp_path / "normalized.jsonl"
    received = {
        "kind": "Request received",
        "pid": 42,
        "context": {"context_id": "id:7"},
        "api_id": "server-api",
        "handler": "serve",
        "message": {"req.Method": "gEt", "req.URL.Path": ""},
    }
    response = {
        "kind": "Response received",
        "pid": 42,
        "context": {"context_id": "id:7"},
        "message": {"req.Method": "gEt", "req.URL.Path": "", "resp.StatusCode": "0200"},
    }
    raw_records = [
        raw_sent,
        received,
        {"kind": "Request routed", "pid": 42, "message": {}},
        {
            "kind": "Request routed",
            "pid": 42,
            "context": {"context_id": "id:7"},
            "message": {
                "req.Method": "gEt",
                "req.URL.Path": "",
                "pattern": "GET /{id}",
            },
        },
        {
            "kind": "Response sent",
            "pid": 42,
            "context": {"context_id": "id:7"},
            "message": {"req.Method": "gEt", "req.URL.Path": "", "code": "103"},
        },
        response,
        response,
    ]
    source.write_text(
        "\n"
        + "\n\n".join(json.dumps(value, ensure_ascii=False) for value in raw_records),
        encoding="utf-8",
    )
    before = source.read_bytes()
    direct = list(iter_raw_events(source))
    assert normalize_file(source, output) == 7
    stored = list(iter_events(output))
    events = [record.event for record in stored]
    assert events == [record.event for record in direct]
    assert [event.kind for event in events] == [
        "send_request",
        "receive_request",
        "request_routed",
        "request_routed",
        "send_response",
        "receive_response",
        "receive_response",
    ]
    assert [record.line for record in direct] == [2, 4, 6, 8, 10, 12, 14]
    assert [record.line for record in stored] == [1, 2, 3, 4, 5, 6, 7]
    assert all(record.path == source for record in direct)
    assert all(record.path == output for record in stored)
    assert stored[0].location == f"{output}:1"
    assert events[0].api_id == " API\n"
    assert events[1].api_id == "server-api"
    assert events[1].handler == "serve"
    assert all(value is None for value in events[2].message.model_dump().values())
    assert events[2].context_key is None
    assert all(event.api_id is None and event.handler is None for event in events[2:])
    assert events[5] == events[6]
    assert source.read_bytes() == before
    assert output.read_bytes().endswith(b"\n")
