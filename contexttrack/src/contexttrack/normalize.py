"""Pure, record-local conversion from raw capture data to normalized v1 events."""

from contexttrack._raw import RawContext, RawEvent, RawRequestID
from contexttrack.models import EVENT_ADAPTER, Event

__all__ = ["normalize_record"]


def _path(value: str | None) -> str | None:
    return "/" if value == "" else value


def _status(value: str | None) -> int | None:
    if value is None:
        return None
    if not value.isdecimal():
        raise ValueError("status code must be a decimal string")
    return int(value)


def normalize_record(record: object) -> Event:
    """Validate and normalize one raw record without mutation, I/O, or association.

    Invalid raw shapes raise Pydantic ValidationError (a ValueError); invalid
    decimal status strings raise ValueError. Incomplete evidence stays null.
    """
    raw = RawEvent.model_validate(record)
    message = raw.message
    # Outbound endpoint labels come only from request_id, never from message.
    labels = raw.request_id or RawRequestID() if raw.kind == "Request sent" else message
    normalized_message: dict[str, object] = {
        "method": labels.method,
        "path": _path(labels.path),
    }
    match raw.kind:
        case "Request sent":
            kind = "send_request"
            normalized_message.update(host=labels.host, raw_query=message.raw_query)
        case "Request received":
            kind = "receive_request"
            normalized_message["raw_query"] = message.raw_query
        case "Request routed":
            kind = "request_routed"
            normalized_message["pattern"] = message.pattern
        case "Response sent":
            kind = "send_response"
            normalized_message["status_code"] = _status(message.code)
        case "Response received":
            kind = "receive_response"
            normalized_message["status_code"] = _status(message.status_code)
    payload = raw.model_dump(exclude={"kind", "message", "context", "request_id"})
    payload.update(
        schema_version=1,
        kind=kind,
        context=(raw.context or RawContext()).model_dump(),
        message=normalized_message,
    )
    return EVENT_ADAPTER.validate_python(payload)
