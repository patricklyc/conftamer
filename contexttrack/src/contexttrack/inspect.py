import argparse
import json
import sys

from contexttrack.capture import Capture, Occurrence, influence_edges, read_capture
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
    dict[
        tuple[MessageLabel, MessageLabel],
        tuple[Occurrence, Occurrence],
    ],
]:
    labels = tuple(dict.fromkeys(item.label for item in capture.occurrences))
    return labels, influence_edges(capture)


def _edge_type(source: Occurrence, target: Occurrence) -> str:
    if (
        source.label.kind == "receive_request"
        and target.label.kind == "send_response"
        and source.exchange_key == target.exchange_key
    ):
        return "request/reply"
    return "declared"


def render_graph_text(capture: Capture) -> str:
    labels, edges = _graph_parts(capture)
    node_ids = {label: index for index, label in enumerate(labels)}
    ordered_edges = sorted(
        edges.items(),
        key=lambda item: (node_ids[item[0][0]], node_ids[item[0][1]]),
    )
    sends_without_sources = sum(
        occurrence.label.kind in ("send_request", "send_response")
        and not occurrence.source_keys
        for occurrence in capture.occurrences
    )

    lines = ["Influence graph (recorded sources)", f"Nodes ({len(labels)}):"]
    lines.extend(f"  n{node_ids[label]} {_label_json(label)}" for label in labels)
    lines.append(f"Edges ({len(ordered_edges)}):")
    for (source_label, target_label), (source, target) in ordered_edges:
        lines.append(
            f"  n{node_ids[source_label]} -> n{node_ids[target_label]} "
            f"{_edge_type(source, target)} source={source.location} "
            f"target={target.location}"
        )
    lines.append(
        f"Summary: occurrences={len(capture.occurrences)} "
        f"nodes={len(labels)} edges={len(ordered_edges)} "
        f"sends_without_sources={sends_without_sources}"
    )
    return "\n".join(lines) + "\n"


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="contexttrack",
        description="Inspect a ContextTrack v4 capture.",
    )
    parser.add_argument("input", help="capture JSONL file or directory")
    return parser


def main() -> None:
    arguments = _parser().parse_args()
    sys.stdout.write(render_graph_text(read_capture(arguments.input)))


if __name__ == "__main__":
    main()
