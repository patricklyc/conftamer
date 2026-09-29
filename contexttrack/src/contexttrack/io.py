"""Strict, location-aware JSONL readers and atomic normalized-file publication."""

import errno
import json
import os
import tempfile
from collections.abc import Callable, Iterable, Iterator
from dataclasses import dataclass
from pathlib import Path

from contexttrack.models import EVENT_ADAPTER, Event
from contexttrack.normalize import normalize_record

__all__ = [
    "EventFileError",
    "LocatedEvent",
    "iter_events",
    "iter_raw_events",
    "normalize_file",
    "write_events",
]


@dataclass(frozen=True)
class LocatedEvent:
    """An event and its physical input location; location is not wire metadata."""

    event: Event
    path: Path
    line: int

    @property
    def location(self) -> str:
        return f"{self.path}:{self.line}"


class EventFileError(ValueError):
    """Invalid input with its path, physical line, and original cause."""

    def __init__(self, path: Path, line: int, reason: str) -> None:
        self.path = path
        self.line = line
        self.reason = reason
        super().__init__(f"{path}:{line}: {reason}")


def _reject_constant(value: str) -> None:
    raise ValueError(f"invalid JSON constant {value}")


def _unique_object(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result: dict[str, object] = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON key {key!r}")
        result[key] = value
    return result


def _iter_json_records(path: Path) -> Iterator[tuple[int, object]]:
    with path.open("rb") as source:
        for line_number, data in enumerate(source, 1):
            try:
                text = data.decode("utf-8", errors="strict")
                if not text.strip():
                    continue
                record = json.loads(
                    text,
                    parse_constant=_reject_constant,
                    object_pairs_hook=_unique_object,
                )
            except ValueError as error:
                # JSONDecodeError and UnicodeDecodeError are ValueError subclasses.
                raise EventFileError(path, line_number, str(error)) from error
            yield line_number, record


def _iter_validated_events(
    path: str | Path, validate: Callable[[object], Event]
) -> Iterator[LocatedEvent]:
    path = Path(path)
    for line_number, record in _iter_json_records(path):
        try:
            event = validate(record)
        except ValueError as error:
            raise EventFileError(path, line_number, str(error)) from error
        yield LocatedEvent(event=event, path=path, line=line_number)


def iter_raw_events(path: str | Path) -> Iterator[LocatedEvent]:
    """Lazily normalize a completed raw capture in memory, without writing it."""
    return _iter_validated_events(path, normalize_record)


def iter_events(path: str | Path) -> Iterator[LocatedEvent]:
    """Lazily read normalized v1 events; raw captures are not accepted."""
    return _iter_validated_events(path, EVENT_ADAPTER.validate_python)


def write_events(events: Iterable[Event], output: str | Path) -> int:
    """Stream validated models to a new file, publishing only a complete output.

    A same-filesystem hard link supplies atomic, no-clobber publication. This is
    not a crash-durability guarantee; unsupported hard links raise OSError.
    """
    output = Path(output)
    if os.path.lexists(output):
        raise FileExistsError(errno.EEXIST, "output already exists", str(output))
    temporary = None
    count = 0
    try:
        with tempfile.NamedTemporaryFile(
            mode="w",
            encoding="utf-8",
            newline="\n",
            dir=output.parent,
            prefix=".contexttrack-",
            suffix=".tmp",
            delete=False,
        ) as target:
            temporary = Path(target.name)
            for value in events:
                event = EVENT_ADAPTER.validate_python(value)
                target.write(
                    event.model_dump_json(
                        by_alias=False, exclude_none=False, ensure_ascii=False
                    )
                    + "\n"
                )
                count += 1
        os.link(temporary, output)
        return count
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def normalize_file(source: str | Path, output: str | Path) -> int:
    """Normalize one completed raw capture into a new file; return its event count."""
    return write_events((record.event for record in iter_raw_events(source)), output)
