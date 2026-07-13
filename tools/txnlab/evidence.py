"""Evidence bundle writer with stable hashes and structured events."""

from __future__ import annotations

import hashlib
import json
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


class EvidenceBundle:
    def __init__(self, root: Path, run_key: str):
        self.root = root / run_key
        self.root.mkdir(parents=True, exist_ok=False)
        self.events_path = self.root / "events.jsonl"
        self.started_at = utc_now()

    def event(self, event: dict[str, Any]) -> None:
        record = {"at": utc_now(), **event}
        with self.events_path.open("a", encoding="utf-8") as fh:
            fh.write(json.dumps(record, sort_keys=True, ensure_ascii=True) + "\n")

    def write_json(self, relative: str | Path, value: Any) -> Path:
        path = self.root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(value, indent=2, sort_keys=True, ensure_ascii=True) + "\n")
        return path

    def write_text(self, relative: str | Path, value: str) -> Path:
        path = self.root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(value)
        return path

    def file_index(self) -> list[dict[str, Any]]:
        files = []
        for path in sorted(item for item in self.root.rglob("*") if item.is_file()):
            digest = hashlib.sha256(path.read_bytes()).hexdigest()
            files.append(
                {
                    "path": str(path.relative_to(self.root)),
                    "bytes": path.stat().st_size,
                    "sha256": digest,
                }
            )
        return files

    def finalize(self, manifest: dict[str, Any]) -> Path:
        manifest = {
            **manifest,
            "started_at": self.started_at,
            "finished_at": utc_now(),
        }
        self.write_json("manifest.json", manifest)
        index = self.file_index()
        return self.write_json("evidence-index.json", {"files": index})
