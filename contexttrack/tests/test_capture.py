import copy
import json
from pathlib import Path

import pytest

from contexttrack.capture import read_capture
from contexttrack.events import RequestEvent

FIXTURE = Path(__file__).parents[1] / "testdata/v2-chain.jsonl"
PROCESS_A = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
PROCESS_B = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"


def fixture_records() -> list[dict[str, object]]:
    return [json.loads(line) for line in FIXTURE.read_text().splitlines()]


def write_records(
    directory: Path,
    records: list[dict[str, object]],
    name: str = "capture.jsonl",
) -> Path:
    path = directory / name
    path.write_text("".join(f"{json.dumps(record)}\n" for record in records))
    return path


def assert_read_error(path: Path, *parts: str) -> str:
    with pytest.raises(ValueError) as caught:
        read_capture(path)
    message = str(caught.value)
    for part in parts:
        assert part in message
    return message


def test_responses_reuse_exact_origin_labels():
    capture = read_capture(FIXTURE)
    received, sent, response_in, response_out = capture.occurrences
    assert response_in.exchange_key == sent.exchange_key
    assert response_out.exchange_key == received.exchange_key
    assert response_in.label.host == "module-c.test"
    assert response_out.label.pattern == "GET /front/{id}"
    assert response_out.label.pattern_dialect == "go_serve_mux"
    assert response_out.label.api_id == "example.org/b"


def test_response_reference_requires_a_declared_exchange(tmp_path):
    records = fixture_records()
    records[3]["exchange_id"] = 99
    path = write_records(tmp_path, records)

    assert_read_error(path, f"{path}:4", "exchange", "99")


def test_response_direction_must_match_its_origin(tmp_path):
    records = fixture_records()
    records[3]["kind"] = "send_response"
    path = write_records(tmp_path, records)

    assert_read_error(path, f"{path}:4", f"{path}:3", "send_request")


def test_sequence_must_be_contiguous_from_one(tmp_path):
    records = fixture_records()
    records[2]["seq"] = 4
    path = write_records(tmp_path, records)

    assert_read_error(path, f"{path}:3", "sequence", "3", "4")


def test_sequence_must_start_at_one(tmp_path):
    record = fixture_records()[0]
    record["seq"] = 2
    path = write_records(tmp_path, [record])

    assert_read_error(path, f"{path}:1", "sequence", "1", "2")


def test_conflicting_api_binding_reports_both_locations(tmp_path):
    records = fixture_records()
    records[0]["api_id"] = "example.org/original"
    path = write_records(tmp_path, records)

    assert_read_error(
        path,
        f"{path}:2",
        f"{path}:1",
        "example.org/original",
        "example.org/b",
    )


def test_exchange_cannot_be_declared_twice(tmp_path):
    records = fixture_records()
    duplicate = copy.deepcopy(records[0])
    duplicate["seq"] = 2
    path = write_records(tmp_path, [records[0], duplicate])

    assert_read_error(path, f"{path}:2", f"{path}:1", "declared")


def test_exchange_has_at_most_one_final_response(tmp_path):
    records = fixture_records()
    duplicate = copy.deepcopy(records[3])
    duplicate["seq"] = 6
    path = write_records(tmp_path, [*records, duplicate])

    assert_read_error(path, f"{path}:6", f"{path}:4", "final response")


def test_response_must_follow_its_origin(tmp_path):
    records = fixture_records()
    response = copy.deepcopy(records[3])
    response["seq"] = 1
    origin = copy.deepcopy(records[2])
    origin["seq"] = 2
    path = write_records(tmp_path, [response, origin])

    assert_read_error(path, f"{path}:1", "preceding", "exchange", "2")


def test_metadata_cannot_target_a_client_exchange(tmp_path):
    records = fixture_records()
    origin = copy.deepcopy(records[2])
    origin["seq"] = 1
    metadata = copy.deepcopy(records[1])
    metadata["seq"] = 2
    metadata["exchange_id"] = origin["exchange_id"]
    path = write_records(tmp_path, [origin, metadata])

    assert_read_error(path, f"{path}:2", f"{path}:1", "metadata", "send_request")


def test_metadata_must_follow_its_origin(tmp_path):
    metadata = fixture_records()[1]
    metadata["seq"] = 1
    path = write_records(tmp_path, [metadata])

    assert_read_error(path, f"{path}:1", "preceding", "exchange", "1")


def test_nonempty_file_has_one_capture_identity(tmp_path):
    records = fixture_records()
    records[1]["capture_id"] = "another-capture"
    path = write_records(tmp_path, records)

    assert_read_error(path, f"{path}:2", f"{path}:1", "capture")


def test_nonempty_file_has_one_process_identity(tmp_path):
    records = fixture_records()
    records[1]["process_id"] = PROCESS_B
    path = write_records(tmp_path, records)

    assert_read_error(path, f"{path}:2", f"{path}:1", "process")


def test_directory_has_one_capture_identity(tmp_path):
    first = fixture_records()[0]
    second = copy.deepcopy(first)
    second["capture_id"] = "another-capture"
    second["process_id"] = PROCESS_B
    write_records(tmp_path, [first], "a.jsonl")
    second_path = write_records(tmp_path, [second], "b.jsonl")

    assert_read_error(tmp_path, f"{second_path}:1", "capture")


def test_process_cannot_be_split_across_files(tmp_path):
    record = fixture_records()[0]
    first_path = write_records(tmp_path, [record], "a.jsonl")
    second_path = write_records(tmp_path, [record], "b.jsonl")

    assert_read_error(
        tmp_path,
        f"{first_path}:1",
        f"{second_path}:1",
        "process",
    )


def test_malformed_json_reports_physical_line_after_blanks(tmp_path):
    path = tmp_path / "capture.jsonl"
    path.write_bytes(b"\n  \nnot-json\n")

    assert_read_error(path, f"{path}:3", "invalid")


def test_missing_field_reports_shape_error_at_location(tmp_path):
    records = fixture_records()
    del records[0]["context_id"]
    path = write_records(tmp_path, records)

    assert_read_error(path, f"{path}:1", "context_id", "Field required")


def test_invalid_utf8_reports_physical_location(tmp_path):
    path = tmp_path / "capture.jsonl"
    line = FIXTURE.read_bytes().splitlines()[0].replace(b"synthetic", b"\xff")
    path.write_bytes(line + b"\n")

    assert_read_error(path, f"{path}:1", "utf-8")


def test_v1_records_are_not_supported(tmp_path):
    records = fixture_records()
    records[0]["schema_version"] = 1
    path = write_records(tmp_path, records[:1])

    assert_read_error(path, f"{path}:1", "schema_version")


def test_request_without_response_is_valid(tmp_path):
    records = fixture_records()[:3]
    path = write_records(tmp_path, records)

    capture = read_capture(path)

    assert [item.label.kind for item in capture.occurrences] == [
        "receive_request",
        "send_request",
    ]


def test_server_request_without_metadata_uses_concrete_path(tmp_path):
    path = write_records(tmp_path, [fixture_records()[0]])

    (occurrence,) = read_capture(path).occurrences

    assert occurrence.label.path == "/front/42"
    assert occurrence.label.pattern is None
    assert occurrence.label.pattern_dialect is None


def test_api_only_metadata_does_not_erase_last_route(tmp_path):
    records = fixture_records()
    origin = records[0]
    route = records[1]
    route["api_id"] = None
    api_only = copy.deepcopy(route)
    api_only["seq"] = 3
    api_only["route"] = None
    api_only["api_id"] = "example.org/b"
    response = records[4]
    response["seq"] = 4
    path = write_records(tmp_path, [origin, route, api_only, response])

    request, final = read_capture(path).occurrences

    assert request.label.pattern == "GET /front/{id}"
    assert final.label.pattern == "GET /front/{id}"
    assert final.label.api_id == "example.org/b"


def test_metadata_after_final_response_still_resolves_labels(tmp_path):
    records = fixture_records()
    origin = records[0]
    response = records[4]
    response["seq"] = 2
    metadata = records[1]
    metadata["seq"] = 3
    path = write_records(tmp_path, [origin, response, metadata])

    request, final = read_capture(path).occurrences

    assert request.label.pattern == "GET /front/{id}"
    assert final.label.pattern == "GET /front/{id}"
    assert final.label.api_id == "example.org/b"


def test_last_unverifiable_route_forces_concrete_path_fallback(tmp_path):
    records = fixture_records()
    unverifiable = copy.deepcopy(records[1])
    unverifiable["seq"] = 3
    route = unverifiable["route"]
    assert isinstance(route, dict)
    route["pattern"] = "GET /rewritten/{id}"
    route["matched_path"] = "/rewritten/42"
    route["full_pattern"] = None
    path = write_records(tmp_path, [records[0], records[1], unverifiable])

    (request,) = read_capture(path).occurrences

    assert request.label.path == "/front/42"
    assert request.label.pattern is None
    assert request.label.pattern_dialect is None


def test_empty_paths_are_normalized_only_in_semantic_labels(tmp_path):
    record = fixture_records()[2]
    request = record["request"]
    assert isinstance(request, dict)
    request["path"] = ""
    record["seq"] = 1
    path = write_records(tmp_path, [record])

    capture = read_capture(path)
    event = capture.records[0].event

    assert isinstance(event, RequestEvent)
    assert event.request.path == ""
    assert capture.occurrences[0].label.path == "/"


def test_unknown_context_and_api_remain_unknown(tmp_path):
    record = fixture_records()[0]
    record["context_id"] = None
    record["api_id"] = None
    path = write_records(tmp_path, [record])

    (occurrence,) = read_capture(path).occurrences

    assert occurrence.context_key is None
    assert occurrence.label.api_id is None


@pytest.mark.parametrize("as_directory", [False, True])
def test_empty_inputs_return_empty_capture(tmp_path, as_directory):
    path = tmp_path
    if not as_directory:
        path = tmp_path / "empty.jsonl"
        path.touch()

    capture = read_capture(path)

    assert capture.records == ()
    assert capture.occurrences == ()


def test_filesystem_errors_are_not_rewritten(tmp_path):
    missing = tmp_path / "missing.jsonl"

    with pytest.raises(FileNotFoundError):
        read_capture(missing)


def test_same_local_ids_in_different_processes_stay_distinct(tmp_path):
    first = fixture_records()[0]
    second = copy.deepcopy(first)
    second["process_id"] = PROCESS_B
    write_records(tmp_path, [first], "a.jsonl")
    write_records(tmp_path, [second], "b.jsonl")

    capture = read_capture(tmp_path)

    assert [item.exchange_key for item in capture.occurrences] == [
        ("synthetic-chain", PROCESS_A, 1),
        ("synthetic-chain", PROCESS_B, 1),
    ]
    assert [item.context_key for item in capture.occurrences] == [
        ("synthetic-chain", PROCESS_A, 7),
        ("synthetic-chain", PROCESS_B, 7),
    ]


def test_interleaved_identical_requests_join_reversed_responses_exactly(tmp_path):
    records = fixture_records()
    first = records[2]
    first["seq"] = 1
    first["exchange_id"] = 10
    second = copy.deepcopy(first)
    second["seq"] = 2
    second["exchange_id"] = 11
    second_response = records[3]
    second_response["seq"] = 3
    second_response["exchange_id"] = 11
    second_response["status_code"] = 201
    first_response = copy.deepcopy(second_response)
    first_response["seq"] = 4
    first_response["exchange_id"] = 10
    first_response["status_code"] = 202
    path = write_records(
        tmp_path,
        [first, second, second_response, first_response],
    )

    capture = read_capture(path)
    responses = capture.occurrences[2:]

    assert [item.exchange_key[-1] for item in responses] == [11, 10]
    assert [item.label.status_code for item in responses] == [201, 202]
    assert all(item.label.host == "module-c.test" for item in responses)
