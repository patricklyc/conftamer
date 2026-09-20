from collections.abc import Iterable
from dataclasses import dataclass
from pathlib import Path

from pydantic import ValidationError

from contexttrack.events import (
    EVENT_ADAPTER,
    Event,
    MessageLabel,
    RequestEvent,
    ResponseEvent,
)

CaptureKey = tuple[str, str, int]


@dataclass(frozen=True)
class Occurrence:
    label: MessageLabel
    context_key: CaptureKey | None
    exchange_key: CaptureKey
    seq: int
    location: str


@dataclass(frozen=True)
class Capture:
    occurrences: tuple[Occurrence, ...]


@dataclass
class _Exchange:
    origin: Occurrence
    response_location: str | None = None


def _key(event: Event) -> CaptureKey:
    return (event.capture_id, event.process_id, event.exchange_id)


def _request_occurrence(event: RequestEvent, location: str) -> Occurrence:
    path = "/" if event.request.path == "" else event.request.path
    label = MessageLabel(
        kind=event.kind,
        method=event.request.method,
        host=event.request.host if event.kind == "send_request" else None,
        path=path,
        status_code=None,
    )
    context_key = (
        None
        if event.context_id is None
        else (event.capture_id, event.process_id, event.context_id)
    )
    return Occurrence(label, context_key, _key(event), event.seq, location)


def _response_occurrence(
    event: ResponseEvent,
    exchange: _Exchange,
    location: str,
) -> Occurrence:
    label = exchange.origin.label.model_copy(
        update={"kind": event.kind, "status_code": event.status_code}
    )
    return Occurrence(
        label,
        exchange.origin.context_key,
        _key(event),
        event.seq,
        location,
    )


def read_capture(path: str | Path) -> Capture:
    capture_path = Path(path)
    paths = (
        sorted(capture_path.glob("*.jsonl"))
        if capture_path.is_dir()
        else [capture_path]
    )
    occurrences: list[Occurrence] = []
    exchanges: dict[CaptureKey, _Exchange] = {}
    capture_identity: tuple[str, str] | None = None
    process_files: dict[str, str] = {}

    for file_path in paths:
        file_identity: tuple[str, str, str] | None = None
        expected_seq = 1
        with file_path.open("rb") as stream:
            for line_number, raw_line in enumerate(stream, 1):
                location = f"{file_path}:{line_number}"
                try:
                    text = raw_line.decode("utf-8")
                except UnicodeDecodeError as error:
                    raise ValueError(f"{location}: {error}") from error
                if not text.strip():
                    continue
                try:
                    event = EVENT_ADAPTER.validate_json(text)
                except ValidationError as error:
                    raise ValueError(f"{location}: {error}") from error

                if file_identity is None:
                    if (
                        capture_identity is not None
                        and event.capture_id != capture_identity[0]
                    ):
                        raise ValueError(
                            f"{location}: capture ID {event.capture_id!r} differs "
                            f"from {capture_identity[0]!r} at {capture_identity[1]}"
                        )
                    previous_file = process_files.get(event.process_id)
                    if previous_file is not None:
                        raise ValueError(
                            f"{location}: process ID {event.process_id!r} already "
                            f"appeared in the file starting at {previous_file}"
                        )
                    file_identity = (
                        event.capture_id,
                        event.process_id,
                        location,
                    )
                    if capture_identity is None:
                        capture_identity = (event.capture_id, location)
                    process_files[event.process_id] = location
                else:
                    if event.capture_id != file_identity[0]:
                        raise ValueError(
                            f"{location}: capture ID {event.capture_id!r} differs "
                            f"from {file_identity[0]!r} at {file_identity[2]}"
                        )
                    if event.process_id != file_identity[1]:
                        raise ValueError(
                            f"{location}: process ID {event.process_id!r} differs "
                            f"from {file_identity[1]!r} at {file_identity[2]}"
                        )
                if event.seq != expected_seq:
                    raise ValueError(
                        f"{location}: sequence expected {expected_seq}, got {event.seq}"
                    )

                key = _key(event)
                if isinstance(event, RequestEvent):
                    previous = exchanges.get(key)
                    if previous is not None:
                        raise ValueError(
                            f"{location}: exchange {event.exchange_id} was already "
                            f"declared at {previous.origin.location}"
                        )
                    occurrence = _request_occurrence(event, location)
                    exchanges[key] = _Exchange(occurrence)
                else:
                    exchange = exchanges.get(key)
                    if exchange is None:
                        raise ValueError(
                            f"{location}: no preceding request declaration for "
                            f"exchange {event.exchange_id}"
                        )
                    expected_kind = (
                        "receive_response"
                        if exchange.origin.label.kind == "send_request"
                        else "send_response"
                    )
                    if event.kind != expected_kind:
                        raise ValueError(
                            f"{location}: {event.kind} cannot reference "
                            f"{exchange.origin.label.kind} declared at "
                            f"{exchange.origin.location}"
                        )
                    if exchange.response_location is not None:
                        raise ValueError(
                            f"{location}: exchange {event.exchange_id} already has "
                            f"a final response at {exchange.response_location}"
                        )
                    occurrence = _response_occurrence(event, exchange, location)
                    exchange.response_location = location

                occurrences.append(occurrence)
                expected_seq += 1

    return Capture(tuple(occurrences))


def shared_context_pairs(
    occurrences: Iterable[Occurrence],
) -> set[tuple[MessageLabel, MessageLabel]]:
    groups: dict[
        CaptureKey,
        tuple[set[MessageLabel], set[MessageLabel]],
    ] = {}
    for occurrence in occurrences:
        if occurrence.context_key is None:
            continue
        receives, sends = groups.setdefault(occurrence.context_key, (set(), set()))
        destination = (
            receives
            if occurrence.label.kind in ("receive_request", "receive_response")
            else sends
        )
        destination.add(occurrence.label)
    return {
        (received, sent)
        for receives, sends in groups.values()
        for received in receives
        for sent in sends
    }
