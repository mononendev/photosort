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
