import subprocess
from pathlib import Path

ROOT = Path(__file__).parents[1]
SCRIPT = ROOT / "apply-go-patch.sh"


def run_script(*arguments: Path) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["bash", str(SCRIPT), *(str(argument) for argument in arguments)],
        cwd=ROOT,
        text=True,
        capture_output=True,
        check=False,
    )


def initialize_repository(path: Path, version: str) -> None:
    path.mkdir()
    subprocess.run(["git", "init", "-q", str(path)], check=True)
    (path / "VERSION").write_text(version + "\n")
    subprocess.run(["git", "-C", str(path), "add", "VERSION"], check=True)
    subprocess.run(
        [
            "git",
            "-C",
            str(path),
            "-c",
            "user.name=ContextTrack Tests",
            "-c",
            "user.email=contexttrack-tests@example.invalid",
            "commit",
            "-q",
            "-m",
            "fixture",
        ],
        check=True,
    )


def test_application_script_requires_exactly_one_destination(tmp_path):
    destination = tmp_path / "go"

    missing = run_script()
    extra = run_script(destination, destination)

    assert missing.returncode != 0
    assert extra.returncode != 0
    assert "usage:" in missing.stderr.lower()
    assert "usage:" in extra.stderr.lower()


def test_application_script_rejects_wrong_go_version(tmp_path):
    destination = tmp_path / "go"
    initialize_repository(destination, "go1.26.5")

    result = run_script(destination)

    assert result.returncode != 0
    assert "go1.26.6" in result.stderr
    assert "go1.26.5" in result.stderr


def test_application_script_rejects_dirty_tree(tmp_path):
    destination = tmp_path / "go"
    initialize_repository(destination, "go1.26.6")
    (destination / "VERSION").write_text("go1.26.6\nchanged\n")

    result = run_script(destination)

    assert result.returncode != 0
    assert "clean" in result.stderr.lower()
