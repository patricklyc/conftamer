from collections.abc import Iterable
from dataclasses import dataclass
from pathlib import Path

from pydantic import ValidationError

from contexttrack.events import (
    EVENT_ADAPTER,
    Event,
    MessageLabel,
    MetadataEvent,
    RequestEvent,
    ResponseEvent,
    Route,
)

CaptureKey = tuple[str, str, int]


@dataclass(frozen=True)
class RecordedEvent:
    event: Event
    location: str


@dataclass(frozen=True)
class Occurrence:
    label: MessageLabel
    context_key: CaptureKey | None
    exchange_key: CaptureKey
    seq: int
    location: str


@dataclass(frozen=True)
class Capture:
    records: tuple[RecordedEvent, ...]
    occurrences: tuple[Occurrence, ...]


@dataclass
class _Exchange:
    origin: RecordedEvent
    route: Route | None
    api_id: str | None
    api_location: str | None
    response: RecordedEvent | None = None


def _key(event: RequestEvent | ResponseEvent | MetadataEvent) -> CaptureKey:
    return (event.capture_id, event.process_id, event.exchange_id)


def _read_file(path: Path) -> list[RecordedEvent]:
    records: list[RecordedEvent] = []
    with path.open("rb") as stream:
        for line_number, raw_line in enumerate(stream, 1):
            location = f"{path}:{line_number}"
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

            if records:
                first = records[0]
                if event.capture_id != first.event.capture_id:
                    raise ValueError(
                        f"{location}: capture ID {event.capture_id!r} differs "
                        f"from {first.event.capture_id!r} at {first.location}"
                    )
                if event.process_id != first.event.process_id:
                    raise ValueError(
                        f"{location}: process ID {event.process_id!r} differs "
                        f"from {first.event.process_id!r} at {first.location}"
                    )
            expected_seq = len(records) + 1
            if event.seq != expected_seq:
                raise ValueError(
                    f"{location}: sequence expected {expected_seq}, got {event.seq}"
                )
            records.append(RecordedEvent(event, location))
    return records


def _read_records(paths: list[Path]) -> list[RecordedEvent]:
    records: list[RecordedEvent] = []
    process_files: dict[str, RecordedEvent] = {}
    for path in paths:
        file_records = _read_file(path)
        if not file_records:
            continue
        first = file_records[0]
        if records and first.event.capture_id != records[0].event.capture_id:
            raise ValueError(
                f"{first.location}: capture ID {first.event.capture_id!r} differs "
                f"from {records[0].event.capture_id!r} at {records[0].location}"
            )
        previous = process_files.get(first.event.process_id)
        if previous is not None:
            raise ValueError(
                f"{first.location}: process ID {first.event.process_id!r} already "
                f"appeared in the file starting at {previous.location}"
            )
        process_files[first.event.process_id] = first
        records.extend(file_records)
    return records


def _bind_api(exchange: _Exchange, api_id: str, location: str) -> None:
    if exchange.api_id is None:
        exchange.api_id = api_id
        exchange.api_location = location
        return
    if exchange.api_id != api_id:
        raise ValueError(
            f"{location}: API binding {api_id!r} conflicts with "
            f"{exchange.api_id!r} at {exchange.api_location}"
        )


def _resolve_exchanges(
    records: list[RecordedEvent],
) -> dict[CaptureKey, _Exchange]:
    exchanges: dict[CaptureKey, _Exchange] = {}
    for record in records:
        event = record.event
        key = _key(event)
        if isinstance(event, RequestEvent):
            previous = exchanges.get(key)
            if previous is not None:
                raise ValueError(
                    f"{record.location}: exchange {event.exchange_id} was already "
                    f"declared at {previous.origin.location}"
                )
            exchanges[key] = _Exchange(
                origin=record,
                route=None,
                api_id=event.api_id,
                api_location=record.location if event.api_id is not None else None,
            )
            continue

        exchange = exchanges.get(key)
        if exchange is None:
            raise ValueError(
                f"{record.location}: no preceding request declaration for "
                f"exchange {event.exchange_id}"
            )
        origin = exchange.origin.event
        assert isinstance(origin, RequestEvent)
        if isinstance(event, ResponseEvent):
            expected_kind = (
                "receive_response"
                if origin.kind == "send_request"
                else "send_response"
            )
            if event.kind != expected_kind:
                raise ValueError(
                    f"{record.location}: {event.kind} cannot reference "
                    f"{origin.kind} declared at {exchange.origin.location}"
                )
            if exchange.response is not None:
                raise ValueError(
                    f"{record.location}: exchange {event.exchange_id} already has "
                    f"a final response at {exchange.response.location}"
                )
            exchange.response = record
            continue

        if origin.kind != "receive_request":
            raise ValueError(
                f"{record.location}: request metadata cannot reference "
                f"{origin.kind} declared at {exchange.origin.location}"
            )
        if event.route is not None:
            exchange.route = event.route
        if event.api_id is not None:
            _bind_api(exchange, event.api_id, record.location)
    return exchanges


def _request_label(exchange: _Exchange) -> MessageLabel:
    event = exchange.origin.event
    assert isinstance(event, RequestEvent)
    host = None
    path = None
    pattern = None
    dialect = None
    if event.kind == "send_request":
        host = event.request.host
        path = "/" if event.request.path == "" else event.request.path
    elif exchange.route is not None and exchange.route.full_pattern is not None:
        pattern = exchange.route.full_pattern
        dialect = exchange.route.dialect
    else:
        path = "/" if event.request.path == "" else event.request.path
    return MessageLabel(
        kind=event.kind,
        api_id=exchange.api_id,
        method=event.request.method,
        host=host,
        path=path,
        pattern=pattern,
        pattern_dialect=dialect,
        status_code=None,
    )


def _response_label(origin: MessageLabel, status_code: int) -> MessageLabel:
    kind = (
        "receive_response" if origin.kind == "send_request" else "send_response"
    )
    return origin.model_copy(update={"kind": kind, "status_code": status_code})


def _occurrences(
    records: list[RecordedEvent],
    exchanges: dict[CaptureKey, _Exchange],
) -> tuple[Occurrence, ...]:
    request_labels = {
        key: _request_label(exchange) for key, exchange in exchanges.items()
    }
    occurrences: list[Occurrence] = []
    for record in records:
        event = record.event
        if isinstance(event, MetadataEvent):
            continue
        key = _key(event)
        origin = exchanges[key].origin.event
        assert isinstance(origin, RequestEvent)
        label = request_labels[key]
        if isinstance(event, ResponseEvent):
            label = _response_label(label, event.status_code)
        context_key = (
            None
            if origin.context_id is None
            else (origin.capture_id, origin.process_id, origin.context_id)
        )
        occurrences.append(
            Occurrence(label, context_key, key, event.seq, record.location)
        )
    return tuple(occurrences)


def read_capture(path: str | Path) -> Capture:
    capture_path = Path(path)
    paths = (
        sorted(capture_path.glob("*.jsonl"))
        if capture_path.is_dir()
        else [capture_path]
    )
    records = _read_records(paths)
    exchanges = _resolve_exchanges(records)
    return Capture(tuple(records), _occurrences(records, exchanges))


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
