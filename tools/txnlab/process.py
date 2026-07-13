"""Subprocess execution with structured event recording."""

from __future__ import annotations

import json
import os
import subprocess
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable, Sequence


@dataclass(frozen=True)
class CommandResult:
    argv: list[str]
    returncode: int
    stdout: str
    stderr: str
    duration_seconds: float


class CommandError(RuntimeError):
    def __init__(self, result: CommandResult):
        super().__init__(
            f"command failed ({result.returncode}): {' '.join(result.argv)}\n"
            f"{result.stderr or result.stdout}"
        )
        self.result = result


class CommandRunner:
    def __init__(self, event_sink: Callable[[dict[str, Any]], None] | None = None):
        self.event_sink = event_sink

    def emit(self, event: dict[str, Any]) -> None:
        if self.event_sink is not None:
            self.event_sink(event)

    def run(
        self,
        argv: Sequence[str | Path],
        *,
        cwd: Path | None = None,
        env: dict[str, str] | None = None,
        input_text: str | None = None,
        timeout: int | float | None = None,
        check: bool = True,
    ) -> CommandResult:
        command = [str(item) for item in argv]
        start = time.monotonic()
        self.emit({"type": "command_start", "argv": command, "cwd": str(cwd) if cwd else None})
        merged_env = os.environ.copy()
        if env:
            merged_env.update({key: str(value) for key, value in env.items()})
        proc = subprocess.run(
            command,
            cwd=cwd,
            env=merged_env,
            input=input_text,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=timeout,
            check=False,
        )
        result = CommandResult(
            argv=command,
            returncode=proc.returncode,
            stdout=proc.stdout,
            stderr=proc.stderr,
            duration_seconds=time.monotonic() - start,
        )
        self.emit(
            {
                "type": "command_finish",
                "argv": command,
                "returncode": proc.returncode,
                "duration_seconds": result.duration_seconds,
            }
        )
        if check and result.returncode != 0:
            raise CommandError(result)
        return result

    def json(self, argv: Sequence[str | Path], **kwargs: Any) -> Any:
        result = self.run(argv, **kwargs)
        return json.loads(result.stdout)
