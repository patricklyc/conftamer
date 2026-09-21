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

RecordKey = tuple[str, str, int]
ExchangeKey = tuple[str, str, int]


@dataclass(frozen=True)
class Occurrence:
    label: MessageLabel
    record_key: RecordKey
    exchange_key: ExchangeKey
    source_keys: tuple[RecordKey, ...]
    location: str


@dataclass(frozen=True)
class Capture:
    occurrences: tuple[Occurrence, ...]


@dataclass
class _Exchange:
    origin: Occurrence
    response_location: str | None = None


def _record_key(event: Event) -> RecordKey:
    return (event.capture_id, event.process_id, event.seq)


def _exchange_key(event: Event) -> ExchangeKey:
    return (event.capture_id, event.process_id, event.exchange_id)


def _request_occurrence(
    event: RequestEvent,
    source_keys: tuple[RecordKey, ...],
    location: str,
) -> Occurrence:
    path = "/" if event.request.path == "" else event.request.path
    label = MessageLabel(
        kind=event.kind,
        method=event.request.method,
        host=event.request.host if event.kind == "send_request" else None,
        path=path,
        status_code=None,
    )
    return Occurrence(
        label,
        _record_key(event),
        _exchange_key(event),
        source_keys,
        location,
    )


def _response_occurrence(
    event: ResponseEvent,
    exchange: _Exchange,
    source_keys: tuple[RecordKey, ...],
    location: str,
) -> Occurrence:
    label = exchange.origin.label.model_copy(
        update={"kind": event.kind, "status_code": event.status_code}
    )
    return Occurrence(
        label,
        _record_key(event),
        _exchange_key(event),
        source_keys,
        location,
    )


def _resolve_sources(
    event: Event,
    records: dict[RecordKey, Occurrence],
    location: str,
) -> tuple[RecordKey, ...]:
    source_keys: list[RecordKey] = []
    for source_seq in event.sources:
        if source_seq >= event.seq:
            raise ValueError(
                f"{location}: source sequence {source_seq} must precede "
                f"target sequence {event.seq}"
            )
        source_key = (event.capture_id, event.process_id, source_seq)
        source = records.get(source_key)
        if source is None:
            raise ValueError(
                f"{location}: source sequence {source_seq} does not identify "
                "a preceding record in this process"
            )
        if source.label.kind not in ("receive_request", "receive_response"):
            raise ValueError(
                f"{location}: source sequence {source_seq} identifies "
                f"{source.label.kind} at {source.location}, not a receive"
            )
        source_keys.append(source_key)
    return tuple(source_keys)


def read_capture(path: str | Path) -> Capture:
    capture_path = Path(path)
    paths = (
        sorted(capture_path.glob("*.jsonl"))
        if capture_path.is_dir()
        else [capture_path]
    )
    occurrences: list[Occurrence] = []
    records: dict[RecordKey, Occurrence] = {}
    exchanges: dict[ExchangeKey, _Exchange] = {}
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

                source_keys = _resolve_sources(event, records, location)
                exchange_key = _exchange_key(event)
                if isinstance(event, RequestEvent):
                    previous = exchanges.get(exchange_key)
                    if previous is not None:
                        raise ValueError(
                            f"{location}: exchange {event.exchange_id} was already "
                            f"declared at {previous.origin.location}"
                        )
                    occurrence = _request_occurrence(event, source_keys, location)
                    exchanges[exchange_key] = _Exchange(occurrence)
                else:
                    exchange = exchanges.get(exchange_key)
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
                    if (
                        event.kind == "send_response"
                        and exchange.origin.record_key not in source_keys
                    ):
                        raise ValueError(
                            f"{location}: send_response sources must include "
                            f"its receive_request sequence "
                            f"{exchange.origin.record_key[2]}"
                        )
                    occurrence = _response_occurrence(
                        event, exchange, source_keys, location
                    )
                    exchange.response_location = location

                occurrences.append(occurrence)
                records[occurrence.record_key] = occurrence
                expected_seq += 1

    return Capture(tuple(occurrences))


def influence_edges(
    capture: Capture,
) -> dict[
    tuple[MessageLabel, MessageLabel],
    tuple[Occurrence, Occurrence],
]:
    records = {occurrence.record_key: occurrence for occurrence in capture.occurrences}
    edges: dict[
        tuple[MessageLabel, MessageLabel],
        tuple[Occurrence, Occurrence],
    ] = {}
    for target in capture.occurrences:
        for source_key in target.source_keys:
            source = records[source_key]
            edges.setdefault((source.label, target.label), (source, target))
    return edges
