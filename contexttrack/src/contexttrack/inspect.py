import argparse
import json
import sys

from contexttrack.capture import (
    Capture,
    CaptureKey,
    Occurrence,
    read_capture,
    shared_context_pairs,
)
from contexttrack.events import MessageLabel


def _scoped_id(key: CaptureKey) -> str:
    return f"{key[0]}/{key[1]}/{key[2]}"


def _label_json(label: MessageLabel) -> str:
    return json.dumps(
        label.model_dump(),
        ensure_ascii=False,
        separators=(",", ":"),
    )


def _occurrence_line(occurrence: Occurrence) -> str:
    return (
        f"  seq={occurrence.seq} "
        f"exchange={_scoped_id(occurrence.exchange_key)} "
        f"{_label_json(occurrence.label)}"
    )


def render_groups(capture: Capture) -> str:
    groups: dict[CaptureKey, list[Occurrence]] = {}
    unknown: list[Occurrence] = []
    for occurrence in capture.occurrences:
        if occurrence.context_key is None:
            unknown.append(occurrence)
        else:
            groups.setdefault(occurrence.context_key, []).append(occurrence)

    lines: list[str] = []
    for context_key, occurrences in groups.items():
        lines.append(
            f"Context {_scoped_id(context_key)} "
            f"({len(occurrences)} occurrences)"
        )
        lines.extend(_occurrence_line(occurrence) for occurrence in occurrences)
        lines.append("")
    if unknown:
        lines.append(f"Unknown context ({len(unknown)} occurrences)")
        lines.extend(_occurrence_line(occurrence) for occurrence in unknown)
        lines.append("")

    lines.append(
        f"{len(capture.occurrences)} occurrences in "
        f"{len(groups)} known context; "
        f"{len(unknown)} unknown-context occurrences"
    )
    return "\n".join(lines) + "\n"


def _graph_parts(
    capture: Capture,
) -> tuple[
    tuple[MessageLabel, ...],
    set[tuple[MessageLabel, MessageLabel]],
]:
    labels = tuple(dict.fromkeys(item.label for item in capture.occurrences))
    return labels, shared_context_pairs(capture.occurrences)


def render_graph_text(capture: Capture) -> str:
    labels, edges = _graph_parts(capture)
    node_ids = {label: index for index, label in enumerate(labels)}
    ordered_edges = sorted(edges, key=lambda edge: (node_ids[edge[0]], node_ids[edge[1]]))

    lines = ["Possible-influence graph (shared context)", f"Nodes ({len(labels)}):"]
    lines.extend(
        f"  n{node_ids[label]} {_label_json(label)}" for label in labels
    )
    lines.append(f"Edges ({len(ordered_edges)}):")
    lines.extend(
        f"  n{node_ids[source]} -> n{node_ids[target]}"
        for source, target in ordered_edges
    )
    lines.append(
        f"Summary: {len(capture.occurrences)} occurrences, "
        f"{len(labels)} unique nodes, "
        f"{len(ordered_edges)} possible-influence edges"
    )
    return "\n".join(lines) + "\n"


def _dot_label(label: MessageLabel) -> str:
    lines = [label.kind]
    for name in (
        "api_id",
        "method",
        "host",
        "path",
        "pattern",
        "pattern_dialect",
        "status_code",
    ):
        value = getattr(label, name)
        if value is not None:
            lines.append(f"{name}: {value}")
    return "\n".join(lines)


def _dot_escape(value: str) -> str:
    return (
        value.replace("\\", "\\\\")
        .replace('"', '\\"')
        .replace("\r", "\\r")
        .replace("\n", "\\n")
        .replace("\t", "\\t")
    )


def render_graph_dot(capture: Capture) -> str:
    labels, edges = _graph_parts(capture)
    node_ids = {label: index for index, label in enumerate(labels)}
    ordered_edges = sorted(edges, key=lambda edge: (node_ids[edge[0]], node_ids[edge[1]]))

    lines = ["digraph possible_influence {", '  node [shape="box"];']
    lines.extend(
        f'  n{node_ids[label]} [label="{_dot_escape(_dot_label(label))}"];'
        for label in labels
    )
    lines.extend(
        f"  n{node_ids[source]} -> n{node_ids[target]};"
        for source, target in ordered_edges
    )
    lines.append("}")
    return "\n".join(lines) + "\n"


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="contexttrack",
        description="Inspect ContextTrack v2 captures.",
    )
    commands = parser.add_subparsers(dest="command", required=True)

    groups = commands.add_parser(
        "groups",
        help="show message occurrences grouped by scoped context",
    )
    groups.add_argument("input", help="capture JSONL file or directory")

    graph = commands.add_parser(
        "graph",
        help="show possible influence from received to sent messages",
    )
    graph.add_argument("input", help="capture JSONL file or directory")
    graph.add_argument(
        "--format",
        choices=("text", "dot"),
        default="text",
        help="output format (default: text)",
    )
    return parser


def main() -> None:
    arguments = _parser().parse_args()
    capture = read_capture(arguments.input)
    if arguments.command == "groups":
        sys.stdout.write(render_groups(capture))
        return

    if arguments.format == "dot":
        sys.stdout.write(render_graph_dot(capture))
        labels, edges = _graph_parts(capture)
        print(
            f"occurrences={len(capture.occurrences)} "
            f"nodes={len(labels)} "
            f"possible_influence_edges={len(edges)}",
            file=sys.stderr,
        )
    else:
        sys.stdout.write(render_graph_text(capture))


if __name__ == "__main__":
    main()
