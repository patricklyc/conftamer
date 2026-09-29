"""Keep the published normalized v1 schema in sync with the public models."""

import json
from pathlib import Path

from contexttrack.models import EVENT_ADAPTER


def test_committed_schema_is_generated_from_models():
    path = Path(__file__).parents[1] / "schemas/contexttrack-event-v1.schema.json"
    assert json.loads(path.read_text(encoding="utf-8")) == EVENT_ADAPTER.json_schema()
