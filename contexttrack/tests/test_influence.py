from dataclasses import FrozenInstanceError
from pathlib import Path

import pytest

from contexttrack.capture import Capture, Occurrence, influence_edges, read_capture
from contexttrack.events import MessageLabel

ROOT = Path(__file__).parents[1]
FIXTURE = ROOT / "testdata/v4-chain.jsonl"
PROCESS = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"


def message_label(kind: str, endpoint: str = "/resource") -> MessageLabel:
    return MessageLabel.model_validate(
        {
            "kind": kind,
            "method": "GET",
            "host": (
                "service.test" if kind in ("send_request", "receive_response") else None
            ),
            "path": endpoint,
            "status_code": 200 if kind.endswith("response") else None,
        }
    )


def occurrence(
    kind: str,
    *,
    seq: int,
    exchange_id: int,
    source_seqs: tuple[int, ...] = (),
    endpoint: str = "/resource",
) -> Occurrence:
    return Occurrence(
        label=message_label(kind, endpoint),
        record_key=("capture", PROCESS, seq),
        exchange_key=("capture", PROCESS, exchange_id),
        source_keys=tuple(("capture", PROCESS, source) for source in source_seqs),
        location=f"capture.jsonl:{seq}",
    )


def test_v4_chain_has_exact_source_witnesses():
    capture = read_capture(FIXTURE)
    edges = influence_edges(capture)

    assert len(capture.occurrences) == 4
    assert {
        (source.record_key[2], target.record_key[2])
        for source, target in edges.values()
    } == {(1, 2), (1, 4), (3, 4)}


def test_context_and_chronology_alone_create_no_edge():
    received = occurrence("receive_request", seq=1, exchange_id=1)
    sent = occurrence("send_request", seq=2, exchange_id=2)

    assert influence_edges(Capture((received, sent))) == {}


def test_equal_label_edges_keep_one_exact_occurrence_witness():
    first_source = occurrence("receive_request", seq=1, exchange_id=1)
    first_target = occurrence("send_request", seq=2, exchange_id=2, source_seqs=(1,))
    repeated_source = occurrence("receive_request", seq=3, exchange_id=3)
    repeated_target = occurrence("send_request", seq=4, exchange_id=4, source_seqs=(3,))

    edges = influence_edges(
        Capture((first_source, first_target, repeated_source, repeated_target))
    )

    assert edges == {
        (first_source.label, first_target.label): (first_source, first_target)
    }


def test_repeated_target_label_does_not_hide_later_dependency():
    first_request = occurrence("send_request", seq=1, exchange_id=1)
    response = occurrence("receive_response", seq=2, exchange_id=1)
    repeated_request = occurrence(
        "send_request", seq=3, exchange_id=2, source_seqs=(2,)
    )

    edges = influence_edges(Capture((first_request, response, repeated_request)))

    assert edges == {
        (response.label, repeated_request.label): (response, repeated_request)
    }
    assert edges[(response.label, repeated_request.label)][1].record_key[2] == 3


def test_capture_and_occurrence_values_are_immutable():
    occurrence_value = occurrence("receive_request", seq=1, exchange_id=1)
    capture = Capture((occurrence_value,))

    assert isinstance(capture.occurrences, tuple)
    assert isinstance(occurrence_value.source_keys, tuple)
    with pytest.raises(FrozenInstanceError):
        capture.occurrences = ()  # ty: ignore[invalid-assignment]
    with pytest.raises(FrozenInstanceError):
        occurrence_value.location = "changed"  # ty: ignore[invalid-assignment]
