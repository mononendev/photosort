from pathlib import Path
from photosort import sidecar


def test_reads_attribute_and_element_forms(tmp_path: Path):
    img = tmp_path / "IMG_1.CR2"; img.write_bytes(b"x")
    (tmp_path / "IMG_1.xmp").write_text('<x:xmpmeta><rdf:Description xmp:Rating="3" xmp:Label="Yellow"/></x:xmpmeta>')
    assert sidecar.read_sidecar(img) == {"rating": 3, "label": "Yellow", "sidecar": "IMG_1.xmp"}
    img2 = tmp_path / "IMG_2.CR2"; img2.write_bytes(b"x")
    (tmp_path / "IMG_2.XMP").write_text('<xmp:Rating>5</xmp:Rating><xmp:Label></xmp:Label>')
    assert sidecar.read_sidecar(img2)["rating"] == 5 and sidecar.read_sidecar(img2)["label"] is None
    assert sidecar.read_sidecar(tmp_path / "nope.CR2") == {}


def test_ingest_lists_each_folder_once(tmp_path: Path, monkeypatch):
    from photosort.db import DB
    imgs = [tmp_path / f"IMG_{i}.jpg" for i in range(5)]
    for p in imgs:
        p.write_bytes(b"x")
    (tmp_path / "IMG_3.xmp").write_text('<x:xmpmeta xmp:Rating="4"/>')
    db = DB(tmp_path / "db.sqlite"); db.add_paths(imgs)
    calls = []
    real = sidecar.os.listdir
    monkeypatch.setattr(sidecar.os, "listdir", lambda d: calls.append(d) or real(d))
    assert sidecar.ingest(db, db.rows("lr_json IS NULL")) == 1 and len(calls) == 1
    assert [r["lr_json"] for r in db.rows()][3] == '{"rating": 4, "label": null, "sidecar": "IMG_3.xmp"}'
    assert db.count("lr_json = '{}'") == 4 and db.rows("lr_json IS NULL") == []
