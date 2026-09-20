import argparse
import json
import sys

from contexttrack.capture import Capture, read_capture, shared_context_pairs
from contexttrack.events import MessageLabel


def _label_json(label: MessageLabel) -> str:
    return json.dumps(
        label.model_dump(),
        ensure_ascii=False,
        separators=(",", ":"),
    )


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
    unknown_context = sum(
        occurrence.context_key is None for occurrence in capture.occurrences
    )

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
        f"Summary: occurrences={len(capture.occurrences)} "
        f"nodes={len(labels)} edges={len(ordered_edges)} "
        f"unknown_context={unknown_context}"
    )
    return "\n".join(lines) + "\n"


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="contexttrack",
        description="Inspect a ContextTrack v3 capture.",
    )
    parser.add_argument("input", help="capture JSONL file or directory")
    return parser


def main() -> None:
    arguments = _parser().parse_args()
    sys.stdout.write(render_graph_text(read_capture(arguments.input)))


if __name__ == "__main__":
    main()
