"""The API container installs requirements.lock.txt with --no-deps (docker/pydeps.sh), so a dependency missing from
the lock is missing at runtime (python-multipart was, and the API crash-looped on startup)."""
import ast
import re
from pathlib import Path

ROOT = Path(__file__).parent.parent


def _name(req: str) -> str:
    return re.split(r"[\[<>=!~; ]", req, 1)[0].strip().lower().replace("_", "-")


def test_every_dependency_is_locked():
    # one-line list in pyproject.toml; parsed without tomllib so this runs on Python 3.10 too
    m = re.search(r"(?m)^dependencies\s*=\s*(\[.*\])\s*$", (ROOT / "pyproject.toml").read_text())
    deps = ast.literal_eval(m.group(1))
    locked = {_name(l) for l in (ROOT / "requirements.lock.txt").read_text().splitlines() if "==" in l}
    missing = sorted(_name(d) for d in deps if _name(d) not in locked)
    assert not missing, f"add to requirements.lock.txt: {missing}"
