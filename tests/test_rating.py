"""Your cull rating: 0-3 set the focus tier, 4 is a banger; any rating marks the photo reviewed and survives re-analysis."""
import pytest

pytest.importorskip("fastapi")
from fastapi.testclient import TestClient  # noqa: E402

from photosort import sort as sorter  # noqa: E402
from photosort.db import DB  # noqa: E402
from photosort.web.app import create_app  # noqa: E402


@pytest.fixture
def client(tmp_path):
    photos = tmp_path / "photos"; photos.mkdir()
    for n in ("a.jpg", "b.jpg"):
        (photos / n).write_bytes(b"\xff\xd8\xff")
    app = create_app(tmp_path / "work", photos)   # no `with`: the job runner never starts
    c = TestClient(app)
    db = DB(tmp_path / "work" / "photosort.db")   # same file the app opened
    db.add_paths([photos / "a.jpg", photos / "b.jpg"])
    for r in db.rows():
        db.set_local(r["id"], {"local_tier": 0, "n_people": 0, "people": []})
    c.db = db
    return c


def ids(c, **q):
    return [it["id"] for it in c.get("/api/images", params=q).json()["items"]]


def test_rating_sets_tier_and_reviewed(client):
    a, b = ids(client)
    d = client.patch(f"/api/images/{a}", json={"rating": 4}).json()
    assert (d["rating"], d["focus_tier"], d["reviewed"]) == (4, 3, True)
    assert d["final"]["banger"] is True
    assert ids(client, reviewed=True) == [a]
    assert ids(client, reviewed=False) == [b]
    assert ids(client, rating=4) == [a]
    assert ids(client, tier=3) == [a]

    d = client.patch(f"/api/images/{a}", json={"rating": 3}).json()
    assert (d["rating"], d["focus_tier"], d["final"]["banger"]) == (3, 3, False)

    d = client.patch(f"/api/images/{a}", json={"rating": 1}).json()
    assert (d["rating"], d["focus_tier"], d["final"]["banger"]) == (1, 1, False)


def test_focus_tier_button_keeps_rating_in_step(client):
    a, _ = ids(client)
    client.patch(f"/api/images/{a}", json={"rating": 4})
    d = client.patch(f"/api/images/{a}", json={"focus_tier": 0}).json()
    assert (d["rating"], d["focus_tier"], d["reviewed"]) == (0, 0, True)


def test_reanalysis_does_not_touch_rating_but_reset_does(client):
    a, _ = ids(client)
    client.patch(f"/api/images/{a}", json={"rating": 2})
    client.db.set_local(a, {"local_tier": 0, "n_people": 0, "people": []})
    client.db.set_vlm(a, {"focus_tier": 0, "keeper": False}, {}, None)
    d = client.get(f"/api/images/{a}").json()
    assert (d["rating"], d["focus_tier"], d["reviewed"]) == (2, 2, True)
    d = client.patch(f"/api/images/{a}", json={"clear": True}).json()
    assert (d["rating"], d["reviewed"], d["focus_tier"]) == (None, False, 0)


def test_lift_after_tagging_marks_verdict_stale_and_keeps_rating(client):
    a, _ = ids(client)
    client.patch(f"/api/images/{a}", json={"rating": 3})
    client.db.set_vlm(a, {"focus_tier": 0, "keeper": False}, {}, None)
    assert client.get(f"/api/images/{a}").json()["vlm_stale"] is False
    client.db.set_local(a, {"local_tier": 3, "n_people": 0, "people": [], "exposure": {"ev": 4.0}})
    d = client.get(f"/api/images/{a}").json()
    assert d["vlm_stale"] is True and d["rating"] == 3
    client.db.set_vlm(a, {"focus_tier": 3, "keeper": True}, {}, None)   # the re-tag
    assert client.get(f"/api/images/{a}").json()["vlm_stale"] is False


def test_stale_model_tier_falls_back_to_local(client):
    """The final tier skips a verdict made on the pre-lift frame, in the detail view and in the SQL filters alike."""
    a, _ = ids(client)
    client.db.set_vlm(a, {"focus_tier": 0, "keeper": False}, {}, None)
    client.db.set_local(a, {"local_tier": 3, "n_people": 0, "people": [], "exposure": {"ev": 4.0}})
    d = client.get(f"/api/images/{a}").json()
    assert (d["focus_tier"], d["focus_tier_vlm"], d["review"]) == (3, 0, False)
    assert a in ids(client, tier=3) and a not in ids(client, tier=0)
    client.db.set_vlm(a, {"focus_tier": 1, "keeper": False}, {}, None)   # the re-tag counts again
    assert client.get(f"/api/images/{a}").json()["focus_tier"] == 1
    assert a in ids(client, tier=1)


def test_rating_out_of_range_rejected(client):
    a, _ = ids(client)
    assert client.patch(f"/api/images/{a}", json={"rating": 5}).status_code == 422
    assert client.patch(f"/api/images/{a}", json={"focus_tier": 4}).status_code == 422


def test_export_carries_label_and_banger(tmp_path):
    rec = {"path": str(tmp_path / "x.jpg"), "focus_tier": 3, "focus_tier_local": 2, "focus_tier_vlm": 3, "review": False,
           "subject": "rider_action", "composition": "full_body", "action": None, "keywords": [], "adjectives": [],
           "description": None, "focus_notes": None, "quality_remarks": None, "quality_score": None, "keeper": None,
           "rating": 4, "banger": True}
    x = sorter.xmp_for(rec)
    assert 'xmp:Label="Blue"' in x and "PhotoSort|Banger" in x
    (tmp_path / "x.jpg").write_bytes(b"")
    counts = sorter.build_tree([rec], tmp_path / "out", "copy")
    assert counts["bangers"] == 1 and (tmp_path / "out" / "bangers" / "x.jpg").exists()


def test_old_three_tier_data_and_config_migrate(tmp_path):
    """Old scale 0/1/2 (+ banger 3) keeps its colors: 1 -> 2 (yellow), 2 -> 3 (green), 3 -> 4 (blue); 0 stays."""
    import json
    import sqlite3
    from photosort import config
    p = tmp_path / "old.db"
    DB(p)
    c = sqlite3.connect(p)
    c.execute("PRAGMA user_version = 0")
    c.execute("INSERT INTO images(path, local_json, vlm_json, override_json, truth_json) VALUES "
              "('/a.jpg', '{\"local_tier\": 2}', '{\"focus_tier\": 1}', '{\"rating\": 3, \"focus_tier\": 2}', '{\"focus_tier\": 0}')")
    c.commit(); c.close()
    r = DB(p).rows()[0]
    assert json.loads(r["local_json"])["local_tier"] == 3 and json.loads(r["vlm_json"])["focus_tier"] == 2
    assert json.loads(r["override_json"]) == {"rating": 4, "focus_tier": 3} and json.loads(r["truth_json"])["focus_tier"] == 0
    assert json.loads(DB(p).rows()[0]["local_json"])["local_tier"] == 3   # runs once

    (tmp_path / "config.json").write_text(json.dumps({"focus": {"tier2_min": 0.04, "tier1_min": 0.01},
                                                      "truth": {"label_tiers": {"Green": 2, "Yellow": 1, "Red": 0}}}))
    cfg = config.load(tmp_path)
    assert (cfg["focus"]["tier3_min"], cfg["focus"]["tier2_min"], cfg["focus"]["tier1_min"]) == (0.04, 0.02, 0.01)
    assert cfg["truth"]["label_tiers"] == {"Green": 3, "Yellow": 2, "Red": 0, "Orange": 1}
    assert config.load(tmp_path)["focus"]["tier2_min"] == 0.02   # written back, not shifted twice
