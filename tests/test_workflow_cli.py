"""The command-line workflow: scan -> local -> calibrate/rescore -> submit (sync backend) -> sort -> status."""
import json

import pytest

from photosort import cli, local
from conftest import FakeDetector, write_config


@pytest.fixture
def work(tmp_path, shoot, fake_backend, monkeypatch):
    wd = tmp_path / "work"
    write_config(wd)
    det = FakeDetector()
    real = local.run_local
    monkeypatch.setattr(local, "run_local", lambda db, cfg, cache, ids, device=None, progress=None, should_stop=None, detector=None:
                        real(db, cfg, cache, ids, device, progress, should_stop, det))

    def run(*argv):
        cli.main(["--workdir", str(wd), *map(str, argv)])
    run.wd, run.photos = wd, shoot
    return run


def _db(work):
    from photosort.db import DB
    return DB(work.wd / "photosort.db")


def test_full_cli_flow(work, capsys, fake_backend):
    work("scan", work.photos, "--skip-raw-dupes")
    assert "found 3 images, 3 new (1 with Lightroom sidecars); total tracked 3" in capsys.readouterr().out
    work("scan", work.photos / "sharp.jpg")
    assert "found 1 images, 0 new" in capsys.readouterr().out

    work("local")
    out = capsys.readouterr().out
    assert "done: 3 ok, 0 errors" in out and "local focus tiers: {'tier0': 2, 'tier1': 0, 'tier2': 0, 'tier3': 1}" in out
    assert sorted(p.name for p in (work.wd / "cache").iterdir())[:3] == ["1.jpg", "1_crop.jpg", "1_thumb.jpg"]
    work("local")
    assert "nothing to do" in capsys.readouterr().out

    work("calibrate", "--metric", "head", "--tiles", 4)
    assert "2 images with a primary-subject head value" in capsys.readouterr().out
    assert (work.wd / "calibration_sheet.jpg").is_file()

    work("submit", "--backend", "ollama", "--concurrency", 2)
    out = capsys.readouterr().out
    assert "images=3" in out and "done: 3 ok, 0 errors" in out and "results so far: 3 images, 3,000 input tokens" in out
    assert len(fake_backend.items) == 3
    work("submit", "--backend", "ollama")
    assert "nothing to submit" in capsys.readouterr().out

    work("sort", work.wd / "out", "--link", "symlink", "--xmp", "outdir")
    out = capsys.readouterr().out
    assert "sorted 3 images" in out and "XMP: wrote 3" in out
    link = work.wd / "out" / "focus_1_soft" / "rider_action" / "full_body" / "sharp.jpg"
    assert link.is_symlink() and link.resolve() == (work.photos / "sharp.jpg").resolve()

    work("status")
    out = capsys.readouterr().out
    assert "3 tracked, 3 local done, 0 in flight, 3 tagged, 0 errors" in out


def test_rescore_after_threshold_change(work, capsys):
    work("scan", work.photos); work("local")
    cfg = json.loads((work.wd / "config.json").read_text())
    cfg["focus"]["tier3_min"] = cfg["focus"]["tier2_min"] = cfg["focus"]["tier1_min"] = 1e9
    (work.wd / "config.json").write_text(json.dumps(cfg))
    capsys.readouterr()
    work("rescore")
    out = capsys.readouterr().out
    assert "rescored; 1 images changed tier" in out and "{'tier0': 3, 'tier1': 0, 'tier2': 0, 'tier3': 0}" in out


def test_submit_sample_and_skip_tier0(work, capsys, fake_backend):
    work("scan", work.photos); work("local")
    work("submit", "--backend", "ollama", "--skip-local-tier0")
    assert len(fake_backend.items) == 1
    work("submit", "--backend", "ollama", "--sample", 1)
    assert len(fake_backend.items) == 2


def test_scan_keeps_raw_without_flag(work, capsys):
    work("scan", work.photos)
    assert "found 4 images" in capsys.readouterr().out
    assert _db(work).count("path LIKE '%.CR2'") == 1


def test_estimate_lists_models(work, capsys):
    work("scan", work.photos)
    work("estimate")
    out = capsys.readouterr().out
    assert "4 images pending" in out and "claude-opus-5" in out and "gemini-3.5-flash" in out
