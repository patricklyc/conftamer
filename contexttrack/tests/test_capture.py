import copy
import json
from pathlib import Path

import pytest

from contexttrack.capture import Occurrence, read_capture, shared_context_pairs
from contexttrack.events import MessageLabel

ROOT = Path(__file__).parents[1]
FIXTURE = ROOT / "testdata/v3-chain.jsonl"
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


def test_v3_chain():
    capture = read_capture(FIXTURE)
    items = capture.occurrences
    assert len(items) == 4
    assert [item.exchange_key[2] for item in items] == [1, 2, 2, 1]
    assert [item.label.status_code for item in items] == [None, None, 204, 200]
    assert items[2].label.host == "module-c.test"
    assert items[2].label.path == "/backend"
    assert items[3].label.host is None
    assert items[3].label.path == "/items/7"
    indices = {item.label: index for index, item in enumerate(items)}
    edges = {(indices[a], indices[b]) for a, b in shared_context_pairs(items)}
    assert edges == {(0, 1), (0, 3), (2, 1), (2, 3)}
    assert shared_context_pairs(reversed(items)) == shared_context_pairs(items)


def test_v2_fixture_is_rejected():
    path = ROOT / "testdata/v2-chain.jsonl"

    assert_read_error(path, f"{path}:1", "schema_version")


def test_response_reference_requires_preceding_request(tmp_path):
    records = fixture_records()
    records[2]["exchange_id"] = 99
    path = write_records(tmp_path, records)

    assert_read_error(path, f"{path}:3", "preceding", "exchange", "99")


def test_response_direction_must_match_origin(tmp_path):
    records = fixture_records()
    records[2]["kind"] = "send_response"
    path = write_records(tmp_path, records)

    assert_read_error(path, f"{path}:3", f"{path}:2", "send_request")


def test_exchange_cannot_be_declared_twice(tmp_path):
    records = fixture_records()
    duplicate = copy.deepcopy(records[0])
    duplicate["seq"] = 2
    path = write_records(tmp_path, [records[0], duplicate])

    assert_read_error(path, f"{path}:2", f"{path}:1", "declared")


def test_exchange_has_at_most_one_final_response(tmp_path):
    records = fixture_records()
    duplicate = copy.deepcopy(records[2])
    duplicate["seq"] = 5
    path = write_records(tmp_path, [*records, duplicate])

    assert_read_error(path, f"{path}:5", f"{path}:3", "final response")


def test_interleaved_requests_join_reversed_responses_exactly(tmp_path):
    records = fixture_records()
    first = records[1]
    first.update(seq=1, exchange_id=10)
    second = copy.deepcopy(first)
    second.update(seq=2, exchange_id=11)
    second_response = records[2]
    second_response.update(seq=3, exchange_id=11, status_code=201)
    first_response = copy.deepcopy(second_response)
    first_response.update(seq=4, exchange_id=10, status_code=202)
    path = write_records(
        tmp_path, [first, second, second_response, first_response]
    )

    responses = read_capture(path).occurrences[2:]

    assert [item.exchange_key[-1] for item in responses] == [11, 10]
    assert [item.label.status_code for item in responses] == [201, 202]
    assert all(item.label.path == "/backend" for item in responses)


@pytest.mark.parametrize(("index", "seq", "expected"), [(1, 4, 2), (0, 2, 1)])
def test_sequence_is_contiguous_from_one(tmp_path, index, seq, expected):
    records = fixture_records()
    records[index]["seq"] = seq
    path = write_records(tmp_path, records)

    assert_read_error(path, f"{path}:{index + 1}", "sequence", str(expected), str(seq))


def test_nonempty_file_has_one_capture_and_process(tmp_path):
    records = fixture_records()
    records[1]["capture_id"] = "another-capture"
    capture_path = write_records(tmp_path, records)
    assert_read_error(capture_path, f"{capture_path}:2", f"{capture_path}:1", "capture")

    records = fixture_records()
    records[1]["process_id"] = PROCESS_B
    process_path = write_records(tmp_path, records, "process.jsonl")
    assert_read_error(process_path, f"{process_path}:2", f"{process_path}:1", "process")


def test_directory_has_one_capture_identity(tmp_path):
    first = fixture_records()[0]
    second = copy.deepcopy(first)
    second.update(capture_id="another-capture", process_id=PROCESS_B)
    write_records(tmp_path, [first], "a.jsonl")
    second_path = write_records(tmp_path, [second], "b.jsonl")

    assert_read_error(tmp_path, f"{second_path}:1", "capture")


def test_process_cannot_be_split_across_files(tmp_path):
    record = fixture_records()[0]
    first_path = write_records(tmp_path, [record], "a.jsonl")
    second_path = write_records(tmp_path, [record], "b.jsonl")

    assert_read_error(tmp_path, f"{first_path}:1", f"{second_path}:1", "process")


def test_same_local_ids_in_different_processes_stay_scoped(tmp_path):
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


def test_missing_field_and_extra_field_report_location(tmp_path):
    missing = fixture_records()[0]
    del missing["context_id"]
    missing_path = write_records(tmp_path, [missing])
    assert_read_error(missing_path, f"{missing_path}:1", "context_id", "Field required")

    extra = fixture_records()[0]
    extra["api_id"] = None
    extra_path = write_records(tmp_path, [extra], "extra.jsonl")
    assert_read_error(extra_path, f"{extra_path}:1", "api_id", "Extra inputs")


def test_malformed_json_uses_physical_line_numbers(tmp_path):
    path = tmp_path / "capture.jsonl"
    path.write_bytes(b"\n  \nnot-json\n")

    assert_read_error(path, f"{path}:3", "invalid")


@pytest.mark.parametrize("payload", [b"\xff", br"\ud800"])
def test_invalid_utf8_and_surrogates_report_location(tmp_path, payload):
    path = tmp_path / "capture.jsonl"
    line = FIXTURE.read_bytes().splitlines()[0].replace(b"synthetic-chain", payload)
    path.write_bytes(line + b"\n")

    assert_read_error(path, f"{path}:1")


def test_request_without_response_is_valid(tmp_path):
    path = write_records(tmp_path, fixture_records()[:2])

    assert [item.label.kind for item in read_capture(path).occurrences] == [
        "receive_request",
        "send_request",
    ]


def test_empty_path_is_normalized_in_semantic_label(tmp_path):
    record = fixture_records()[1]
    record["seq"] = 1
    request = record["request"]
    assert isinstance(request, dict)
    request["path"] = ""
    path = write_records(tmp_path, [record])

    assert read_capture(path).occurrences[0].label.path == "/"


def test_unknown_context_remains_unknown(tmp_path):
    record = fixture_records()[0]
    record["context_id"] = None
    path = write_records(tmp_path, [record])

    assert read_capture(path).occurrences[0].context_key is None


@pytest.mark.parametrize("as_directory", [False, True])
def test_empty_inputs_return_empty_capture(tmp_path, as_directory):
    path = tmp_path
    if not as_directory:
        path = tmp_path / "empty.jsonl"
        path.touch()

    assert read_capture(path).occurrences == ()


def test_filesystem_errors_are_not_rewritten(tmp_path):
    with pytest.raises(FileNotFoundError):
        read_capture(tmp_path / "missing.jsonl")


def message_label(kind: str, endpoint: str = "/resource") -> MessageLabel:
    return MessageLabel.model_validate(
        {
            "kind": kind,
            "method": "GET",
            "host": (
                "service.test"
                if kind in ("send_request", "receive_response")
                else None
            ),
            "path": endpoint,
            "status_code": 200 if kind.endswith("response") else None,
        }
    )


def occurrence(
    kind: str,
    *,
    context_key: tuple[str, str, int] | None,
    exchange_id: int,
    seq: int,
    endpoint: str = "/resource",
) -> Occurrence:
    capture_id = context_key[0] if context_key is not None else "capture"
    process_id = context_key[1] if context_key is not None else PROCESS_A
    return Occurrence(
        label=message_label(kind, endpoint),
        context_key=context_key,
        exchange_key=(capture_id, process_id, exchange_id),
        seq=seq,
        location=f"capture.jsonl:{seq}",
    )


def test_pairs_deduplicate_repeated_labels():
    context = ("capture", PROCESS_A, 1)
    sent = occurrence("send_request", context_key=context, exchange_id=1, seq=1)
    received = occurrence(
        "receive_request", context_key=context, exchange_id=2, seq=2
    )
    repeated = occurrence("send_request", context_key=context, exchange_id=3, seq=3)

    assert shared_context_pairs([sent, received, repeated]) == {
        (received.label, sent.label)
    }


def test_pairs_include_same_exchange_and_earlier_send():
    context = ("capture", PROCESS_A, 1)
    sent = occurrence("send_request", context_key=context, exchange_id=7, seq=1)
    received = occurrence(
        "receive_response", context_key=context, exchange_id=7, seq=2
    )

    assert shared_context_pairs([sent, received]) == {(received.label, sent.label)}


def test_pairs_ignore_unknown_contexts_and_isolates():
    occurrences = [
        occurrence("receive_request", context_key=None, exchange_id=1, seq=1),
        occurrence("send_request", context_key=None, exchange_id=2, seq=2),
        occurrence(
            "receive_request",
            context_key=("capture", PROCESS_A, 3),
            exchange_id=3,
            seq=3,
        ),
        occurrence(
            "send_request",
            context_key=("capture", PROCESS_A, 4),
            exchange_id=4,
            seq=4,
        ),
    ]

    assert shared_context_pairs(occurrences) == set()


def test_pairs_scope_reused_context_counters():
    received = occurrence(
        "receive_request",
        context_key=("capture-a", PROCESS_A, 1),
        exchange_id=1,
        seq=1,
    )
    other_capture = occurrence(
        "send_request",
        context_key=("capture-b", PROCESS_A, 1),
        exchange_id=1,
        seq=1,
    )
    other_process = occurrence(
        "send_response",
        context_key=("capture-a", PROCESS_B, 1),
        exchange_id=1,
        seq=1,
    )

    assert shared_context_pairs([received, other_capture, other_process]) == set()
