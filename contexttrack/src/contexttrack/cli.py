"""Command-line entry point for normalization and the model-generated schema."""

import argparse
import json
import sys
from collections.abc import Sequence
from pathlib import Path

from contexttrack.io import EventFileError, normalize_file
from contexttrack.models import EVENT_ADAPTER


def main(argv: Sequence[str] | None = None) -> int:
    """Run the CLI, returning 2 for expected validation or filesystem failures."""
    parser = argparse.ArgumentParser(prog="contexttrack")
    commands = parser.add_subparsers(dest="command", required=True)
    normalize = commands.add_parser(
        "normalize", help="Normalize one completed raw capture"
    )
    normalize.add_argument("input", type=Path)
    normalize.add_argument("--output", type=Path, required=True)
    commands.add_parser("schema", help="Print the normalized event JSON Schema")
    args = parser.parse_args(argv)
    try:
        if args.command == "schema":
            print(
                json.dumps(
                    EVENT_ADAPTER.json_schema(),
                    ensure_ascii=False,
                    indent=2,
                    sort_keys=True,
                )
            )
        else:
            normalize_file(args.input, args.output)
    except (EventFileError, OSError) as error:
        print(f"contexttrack: {error}", file=sys.stderr)
        return 2
    return 0
