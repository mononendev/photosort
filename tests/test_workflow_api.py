"""The web workflow end to end: browse, process, watch the job, review and rate, calibrate, export.

The real local stage runs (image loading, sharpness, tiers, cache files); only the person detector and the vision
model are stubbed (see conftest.py). Jobs run synchronously through the app's own runner."""
import csv
import io
import json
import zipfile

import pytest

pytest.importorskip("fastapi")
from fastapi.testclient import TestClient  # noqa: E402

from photosort.web.app import create_app  # noqa: E402
from conftest import FakeDetector, vlm_answer, write_config  # noqa: E402


@pytest.fixture
def api(tmp_path, shoot, fake_backend):
    write_config(tmp_path / "work")
    app = create_app(tmp_path / "work", shoot)       # no `with`: the runner thread never starts
    c = TestClient(app)
    c.db, c.runner, c.photos, c.work, c.backend = app.state.db, app.state.runner, shoot, tmp_path / "work", fake_backend
    c.runner._detector = FakeDetector()

    def run(paths, **opts):
        job = c.post("/api/jobs", json={"paths": paths, **opts}).json()
        c.runner.run_job(c.db.job(job["id"]))
        return c.get(f"/api/jobs/{job['id']}").json()
    c.run = run
    return c


def by_name(c, **q):
    return {it["name"]: it for it in c.get("/api/images", params={"limit": 500, **q}).json()["items"]}


def test_browse_before_processing(api):
    t = api.get("/api/tree").json()
    assert t["path"] == "" and [d["name"] for d in t["dirs"]] == ["day_2"]
    assert t["dirs"][0]["images_direct"] == 1 and t["dirs"][0]["tracked"] == 0
    assert sorted(f["name"] for f in t["files"]) == ["sharp.jpg", "soft.CR2", "soft.jpg"]
    assert {f["status"] for f in t["files"]} == {"untracked"}
    assert api.get("/api/tree", params={"path": "../.."}).status_code == 400
    assert api.get("/api/tree", params={"path": "sharp.jpg"}).status_code == 404


def test_process_everything(api):
    j = api.run([""])
    assert (j["state"], j["stage"], j["total"], j["done"], j["errors"]) == ("done", "done", 3, 3, 0)
    assert list(j["stages"]) == ["scan", "local", "vlm"] and j["stages"]["scan"]["files"] == 3   # CR2 twin skipped
    assert j["options"]["vlm"] is True and j["message"].startswith("local 3/3 · ollama/fake-vl")

    imgs = by_name(api)
    assert set(imgs) == {"sharp.jpg", "soft.jpg", "empty.jpg"}
    assert all(i["status"] == "tagged" for i in imgs.values())
    s, soft, e = imgs["sharp.jpg"], imgs["soft.jpg"], imgs["empty.jpg"]
    assert (s["focus_tier_local"], soft["focus_tier_local"], e["focus_tier_local"]) == (3, 0, 0)
    assert s["has_crop"] and not e["has_crop"]
    assert (s["lr_rating"], s["lr_label"]) == (3, "Green") and soft["lr_rating"] is None   # sidecar picked up
    assert s["review"] and soft["review"]          # local 3/0 vs the model's 1
    assert s["folder"] == "" and e["folder"] == "day_2"

    for kind in ("thumb", "frame", "crop", "full"):
        r = api.get(f"/media/{kind}/{s['id']}")
        assert r.status_code == 200 and r.headers["content-type"] == "image/jpeg"
    assert api.get(f"/media/crop/{e['id']}").status_code == 404

    # the vision model saw the frame, the crop and the detector summary
    item = next(i for i in api.backend.items if i.key == str(s["id"]))
    assert item.crop and "Local focus guess: tier 3" in item.context

    t = api.get("/api/tree").json()
    assert t["dirs"][0]["tracked"] == 1 and t["dirs"][0]["vlm_done"] == 1
    assert {f["name"]: f["status"] for f in t["files"]}["soft.CR2"] == "untracked"


def test_job_detail_and_items(api):
    j = api.run([""])
    d = api.get(f"/api/jobs/{j['id']}/detail").json()
    assert set(d["stats"]) == {"local", "vlm"} and d["stats"]["vlm"]["n"] == 3
    v = d["stats"]["vlm"]
    assert (v["tokens_in"], v["tokens_out"], v["avg_in"], v["avg_prefill_s"]) == (3000, 600, 1000, 0.1)
    assert v["tok_s"] == 500.0 and d["active"] == [] and len(d["series"]) == 6
    few = api.get(f"/api/jobs/{j['id']}/detail", params={"points": 2}).json()["series"]
    assert [p["stage"] for p in few] == ["local", "local", "vlm", "vlm"]   # capped per stage, not overall
    items = api.get(f"/api/jobs/{j['id']}/items", params={"stage": "local"}).json()
    assert items["total"] == 3 and {i["local"]["local_tier"] for i in items["items"]} == {0, 3}
    vi = api.get(f"/api/jobs/{j['id']}/items", params={"stage": "vlm", "limit": 1}).json()
    assert vi["total"] == 3 and len(vi["items"]) == 1 and vi["items"][0]["vlm"]["primary_subject"] == "rider_action"
    assert [x["id"] for x in api.get("/api/jobs").json()] == [j["id"]]
    assert api.get("/api/jobs/999").status_code == 404


def test_local_only_then_vlm_later(api):
    j = api.run(["day_2"], vlm=False)
    assert (j["state"], j["total"], j["done"]) == ("done", 1, 1) and "vlm" not in j["stages"]
    assert [i["status"] for i in by_name(api).values()] == ["analyzed"]
    j2 = api.run(["day_2"])                          # nothing new locally; the model tags it
    assert j2["message"].startswith("local: nothing new · ") and by_name(api)["empty.jpg"]["status"] == "tagged"
    j3 = api.run(["day_2"])
    assert j3["message"].endswith("nothing new to tag")


def test_vlm_errors_are_counted_and_retried(api):
    api.run([""], vlm=False)
    ids = {n: i["id"] for n, i in by_name(api).items()}
    api.backend.answers[ids["soft.jpg"]] = "ollama parse: bad json"
    j = api.run([""])
    assert (j["errors"], by_name(api, status="error")["soft.jpg"]["error"]) == (1, "ollama parse: bad json")
    del api.backend.answers[ids["soft.jpg"]]
    assert api.run([""])["message"].endswith("nothing new to tag")           # errors aren't retried by default
    j = api.run([""], retry_errors=True)
    assert j["errors"] == 0 and by_name(api, status="error") == {}


def test_skip_tier0(api):
    api.run([""], skip_tier0=True)
    assert {n: i["status"] for n, i in by_name(api).items()} == {"sharp.jpg": "tagged", "soft.jpg": "analyzed", "empty.jpg": "analyzed"}


def test_cancel_queued_job(api):
    job = api.post("/api/jobs", json={"paths": [""]}).json()
    assert api.post(f"/api/jobs/{job['id']}/cancel").json()["state"] == "cancelled"
    assert api.db.next_queued_job() is None
    assert api.post("/api/jobs", json={"paths": []}).status_code == 400
    assert api.post("/api/jobs", json={"paths": ["/etc"]}).status_code == 400


def test_image_filters_and_sorting(api):
    api.run([""])
    ids = {n: i["id"] for n, i in by_name(api).items()}
    api.backend.answers.clear()
    assert set(by_name(api, folder="day_2")) == {"empty.jpg"}
    assert set(by_name(api, folder="", recursive=False)) == {"sharp.jpg", "soft.jpg", "empty.jpg"}
    assert set(by_name(api, tier=1)) == {"sharp.jpg", "soft.jpg", "empty.jpg"}     # the model's tier wins
    assert set(by_name(api, review=True)) == {"sharp.jpg", "soft.jpg", "empty.jpg"}
    assert set(by_name(api, lr_rating=3)) == {"sharp.jpg"} and set(by_name(api, lr_label="Green")) == {"sharp.jpg"}
    assert set(by_name(api, q="day_2")) == {"empty.jpg"} and set(by_name(api, q="onewheel")) == set(ids)
    assert set(by_name(api, keeper=True)) == set(ids)
    assert set(by_name(api, subject="rider_action")) == set(ids)
    order = [it["name"] for it in api.get("/api/images", params={"sort": "sharpness"}).json()["items"]]
    assert order[0] == "sharp.jpg"
    page = api.get("/api/images", params={"limit": 2, "offset": 2}).json()
    assert page["total"] == 3 and len(page["items"]) == 1

    api.patch(f"/api/images/{ids['soft.jpg']}", json={"keeper": False, "note": "meh"})
    assert set(by_name(api, keeper=False)) == {"soft.jpg"}
    assert by_name(api)["soft.jpg"]["overridden"] and not by_name(api)["soft.jpg"]["reviewed"]
    api.patch(f"/api/images/{ids['sharp.jpg']}", json={"rating": 0})
    assert set(by_name(api, tier=0)) == {"sharp.jpg"} and set(by_name(api, reviewed=True)) == {"sharp.jpg"}


def test_image_detail_and_vlm_request(api):
    api.run([""])
    s = by_name(api)["sharp.jpg"]
    d = api.get(f"/api/images/{s['id']}").json()
    assert d["local"]["local_tier"] == 3 and d["vlm"]["focus_tier"] == 1 and d["usage"]["in"] == 1000
    assert d["final"]["focus_tier"] == 1 and d["override"] is None
    r = api.get(f"/api/images/{s['id']}/vlm-request").json()
    assert r["model"] == "fake-vl" and [i["label"] for i in r["images"]] == ["frame", "crop"]
    assert r["request"]["images"][0].startswith("<") and "Local focus guess" in r["context"]
    assert api.get("/api/images/999").status_code == 404


def test_stats(api):
    api.run([""])
    st = api.get("/api/stats").json()
    assert (st["tracked"], st["analyzed"], st["tagged"], st["errors"], st["review"], st["keepers"]) == (3, 3, 3, 0, 3, 3)
    assert st["tiers"] == {"tier0": 0, "tier1": 3, "tier2": 0, "tier3": 0}
    assert st["lr_rated"] == 1 and st["lr_by_tier"] == [{"tier": 1, "rating": 3, "n": 1}]


def test_stats_tiers_with_partial_overrides(api):
    api.run([""])
    ids = {n: i["id"] for n, i in by_name(api).items()}
    api.patch(f"/api/images/{ids['sharp.jpg']}", json={"rating": 4})
    api.patch(f"/api/images/{ids['soft.jpg']}", json={"note": "only a note"})
    assert api.get("/api/stats").json()["tiers"] == {"tier0": 0, "tier1": 2, "tier2": 0, "tier3": 1}


def test_focus_source_drives_stats_filter_and_tiles(api):
    api.run([""])   # local: sharp 3, soft 0, empty 0; the model says 1 for all three
    tiers = lambda: {k: v for k, v in api.get("/api/stats").json()["tiers"].items() if v}
    assert tiers() == {"tier1": 3}
    api.put("/api/config", json={"values": {"focus_source": "strict"}})
    assert tiers() == {"tier0": 2, "tier1": 1}
    assert set(by_name(api, tier=1)) == {"sharp.jpg"} and set(by_name(api, tier=0)) == {"soft.jpg", "empty.jpg"}
    assert {n: i["focus_tier"] for n, i in by_name(api).items()} == {"sharp.jpg": 1, "soft.jpg": 0, "empty.jpg": 0}
    api.put("/api/config", json={"values": {"focus_source": "local"}})
    assert tiers() == {"tier0": 2, "tier3": 1} and set(by_name(api, tier=3)) == {"sharp.jpg"}


def test_config_and_rescore(api):
    api.run([""], vlm=False)
    assert api.get("/api/config").json()["focus"]["tier3_min"] == 0.03
    cfg = api.put("/api/config", json={"values": {"focus": {"tier3_min": 1e9, "tier2_min": 1e9, "tier1_min": 1e9}}}).json()
    assert cfg["focus"]["tier3_min"] == 1e9 and cfg["focus"]["tier1_min"] == 1e9 and cfg["focus"]["use_hf"] is True
    assert json.loads((api.work / "config.json").read_text())["focus"]["tier3_min"] == 1e9
    res = api.post("/api/rescore").json()
    assert res["changed"] == 1 and res["errors"] == 0
    assert by_name(api)["sharp.jpg"]["focus_tier"] == 0


def test_calibration_and_ground_truth(api):
    api.run([""], vlm=False)
    cal = api.get("/api/calibration", params={"metric": "head"}).json()
    assert cal["count"] == 2 and [s["tier"] for s in cal["samples"]] == [0, 3] and "p50" in cal["percentiles"]
    assert api.get("/api/calibration", params={"metric": "eye"}).json()["samples"] == []
    assert api.get("/api/calibration", params={"metric": "nope"}).status_code == 400

    csv_text = "name,label\nsharp.jpg,Green\nsoft.jpg,Red\nmissing.jpg,Red\n"
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w") as z:
        z.writestr("xmp/empty.xmp", '<x:xmpmeta xmp:Rating="1"/>')
    r = api.post("/api/truth/upload", files=[("files", ("v.csv", csv_text.encode())), ("files", ("x.zip", buf.getvalue()))]).json()
    assert (r["verdicts"], r["matched"], r["unmatched"]) == (4, 3, 1)
    s = r["summary"]
    assert s["images_with_truth"] == 3 and s["local"]["accuracy"] == 1.0 and s["vlm"]["accuracy"] is None
    assert set(by_name(api, truth_tier=3)) == {"sharp.jpg"} and by_name(api, truth_mismatch=True) == {}

    (api.photos / "verdicts").mkdir()
    (api.photos / "verdicts" / "v.csv").write_text("name,focus_tier\nsoft.jpg,2\n")
    r = api.post("/api/truth/import", json={"dir": "verdicts", "folder": ""}).json()
    assert r["matched"] == 1 and set(by_name(api, truth_mismatch=True)) == {"soft.jpg"}
    assert api.post("/api/truth/import", json={"dir": "nope"}).status_code == 404
    assert api.delete("/api/truth").json() == {"cleared": 3} and api.get("/api/truth").json()["images_with_truth"] == 0


def test_export(api):
    api.run([""])
    ids = {n: i["id"] for n, i in by_name(api).items()}
    api.patch(f"/api/images/{ids['sharp.jpg']}", json={"rating": 4})
    api.patch(f"/api/images/{ids['soft.jpg']}", json={"rating": 0})
    r = api.post("/api/export", json={"name": "../evil/cull"}).json()
    out = api.work / "exports" / "cull"
    assert r["out"] == str(out) and r["images"] == 3 and r["xmp_written"] == 3
    assert r["tree"] == {"focus_3_sharp": 1, "focus_0_miss": 1, "focus_1_soft": 1, "review": 3, "bangers": 1}
    assert (out / "focus_3_sharp" / "rider_action" / "full_body" / "sharp.jpg").is_file()
    assert (out / "bangers" / "sharp.jpg").is_file() and (out / "review" / "local0_vlm1" / "soft.jpg").is_file()
    rows = {row["path"].rsplit("/", 1)[1]: row for row in csv.DictReader((out / "results.csv").open())}
    assert (rows["sharp.jpg"]["rating"], rows["sharp.jpg"]["focus_tier"], rows["soft.jpg"]["rating"]) == ("4", "3", "0")
    assert len((out / "results.jsonl").read_text().splitlines()) == 3
    x = (out / "xmp" / "sharp.xmp").read_text()
    assert 'xmp:Label="Blue"' in x and "PhotoSort|Banger" in x and 'xmp:Rating="4"' in x
    assert [e["name"] for e in api.get("/api/exports").json()] == ["cull"]

    r = api.post("/api/export", json={"name": "day2", "folder": "day_2", "tree": False, "xmp": False, "focus_source": "local"}).json()
    assert (r["images"], r["tree"], r["xmp_written"]) == (1, {}, 0)


def test_health(api):
    h = api.get("/api/health").json()
    assert h["ok"] and h["current_job"] is None and h["device"] == "cpu" and h["backend"] == "ollama"


def test_vlm_answer_fixture_is_schema_valid():
    assert vlm_answer(primary_subject="bogus")["primary_subject"] == "other"


def test_folder_filters_treat_underscore_literally(api):
    (api.photos / "dayX2" / "sub").mkdir(parents=True)
    (api.photos / "day_2" / "empty.jpg").rename(api.photos / "dayX2" / "sub" / "empty.jpg")
    (api.photos / "day_2" / "other.jpg").write_bytes((api.photos / "sharp.jpg").read_bytes())
    api.run([""], vlm=False)
    assert set(by_name(api, folder="day_2")) == {"other.jpg"}        # LIKE 'day_2/%' would also match dayX2/sub
    assert api.post("/api/export", json={"name": "d", "folder": "day_2", "tree": False, "xmp": False}).json()["images"] == 1
    t = {d["name"]: d for d in api.get("/api/tree").json()["dirs"]}
    assert (t["day_2"]["tracked"], t["dayX2"]["tracked"]) == (1, 1)


def test_stats_keepers_follow_overrides(api):
    api.run([""])
    ids = {n: i["id"] for n, i in by_name(api).items()}
    api.patch(f"/api/images/{ids['soft.jpg']}", json={"keeper": False})
    assert api.get("/api/stats").json()["keepers"] == 2 == len(by_name(api, keeper=True))
