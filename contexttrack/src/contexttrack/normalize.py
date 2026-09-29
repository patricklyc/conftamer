"""Pure, record-local conversion from raw capture data to normalized v1 events."""

from contexttrack._raw import RawContext, RawEvent, RawRequestID
from contexttrack.models import EVENT_ADAPTER, Event

__all__ = ["normalize_record"]

_KINDS = {
    "Request sent": "send_request",
    "Request received": "receive_request",
    "Request routed": "request_routed",
    "Response sent": "send_response",
    "Response received": "receive_response",
}


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
    normalized_message: dict[str, object]
    if raw.kind == "Request sent":
        label = raw.request_id or RawRequestID()
        normalized_message = {
            "method": label.method,
            "host": label.host,
            "path": _path(label.path),
            "raw_query": message.raw_query,
        }
    else:
        normalized_message = {"method": message.method, "path": _path(message.path)}
        if raw.kind == "Request received":
            normalized_message["raw_query"] = message.raw_query
        elif raw.kind == "Request routed":
            normalized_message["pattern"] = message.pattern
        else:
            status = (
                message.code if raw.kind == "Response sent" else message.status_code
            )
            normalized_message["status_code"] = _status(status)
    payload = raw.model_dump(exclude={"kind", "message", "context", "request_id"})
    payload.update(
        schema_version=1,
        kind=_KINDS[raw.kind],
        context=(raw.context or RawContext()).model_dump(),
        message=normalized_message,
    )
    return EVENT_ADAPTER.validate_python(payload)
