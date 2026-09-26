"""Your cull rating: 0-2 set the focus tier, 3 is a banger; any rating marks the photo reviewed and survives re-analysis."""
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
    d = client.patch(f"/api/images/{a}", json={"rating": 3}).json()
    assert (d["rating"], d["focus_tier"], d["reviewed"]) == (3, 2, True)
    assert d["final"]["banger"] is True
    assert ids(client, reviewed=True) == [a]
    assert ids(client, reviewed=False) == [b]
    assert ids(client, rating=3) == [a]
    assert ids(client, tier=2) == [a]

    d = client.patch(f"/api/images/{a}", json={"rating": 1}).json()
    assert (d["rating"], d["focus_tier"], d["final"]["banger"]) == (1, 1, False)


def test_focus_tier_button_keeps_rating_in_step(client):
    a, _ = ids(client)
    client.patch(f"/api/images/{a}", json={"rating": 3})
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


def test_rating_out_of_range_rejected(client):
    a, _ = ids(client)
    assert client.patch(f"/api/images/{a}", json={"rating": 4}).status_code == 422


def test_export_carries_label_and_banger(tmp_path):
    rec = {"path": str(tmp_path / "x.jpg"), "focus_tier": 2, "focus_tier_local": 1, "focus_tier_vlm": 2, "review": False,
           "subject": "rider_action", "composition": "full_body", "action": None, "keywords": [], "adjectives": [],
           "description": None, "focus_notes": None, "quality_remarks": None, "quality_score": None, "keeper": None,
           "rating": 3, "banger": True}
    x = sorter.xmp_for(rec)
    assert 'xmp:Label="Blue"' in x and "PhotoSort|Banger" in x
    (tmp_path / "x.jpg").write_bytes(b"")
    counts = sorter.build_tree([rec], tmp_path / "out", "copy")
    assert counts["bangers"] == 1 and (tmp_path / "out" / "bangers" / "x.jpg").exists()
