from photosort import truth

CFG = {"truth": {"label_tiers": {"Green": 3, "Yellow": 2, "Orange": 1, "Red": 0}, "rating_tiers": {"5": 3, "4": 3, "3": 2, "2": 1, "1": 0, "0": None}}}


def test_parse_xmp_with_keywords_and_tier_resolution():
    x = '''<rdf:Description xmp:Rating="5" xmp:Label="Green"><dc:subject><rdf:Bag><rdf:li>onewheel</rdf:li><rdf:li>focus:1</rdf:li></rdf:Bag></dc:subject></rdf:Description>'''
    v = truth.parse_xmp(x)
    assert v["rating"] == 5 and v["label"] == "Green" and v["keywords"] == ["onewheel", "focus:1"]
    assert truth.resolve_tier(v, CFG) == 1                       # explicit keyword wins
    assert truth.resolve_tier({"label": "Red"}, CFG) == 0         # label
    assert truth.resolve_tier({"label": "Orange"}, CFG) == 1
    assert truth.resolve_tier({"rating": 4}, CFG) == 3            # rating
    assert truth.resolve_tier({"keywords": ["focus3"]}, CFG) == 3
    assert truth.resolve_tier({"rating": 0}, CFG) is None


def test_parse_csv_and_files():
    csv = "name,rating,focus_tier,keywords\nIMG_1.CR2,4,3,a;b\nIMG_2.cr2,,0,\n"
    v = truth.parse_files([("truth.csv", csv.encode()), ("IMG_3.xmp", b'<x xmp:Rating="2"/>')])
    assert v["img_1"]["focus_tier"] == 3 and v["img_1"]["keywords"] == ["a", "b"]
    assert v["img_2"]["focus_tier"] == 0 and v["img_3"]["rating"] == 2


def test_suggest_thresholds_separates_classes():
    pairs = ([(0.001 * i, 0) for i in range(1, 15)] + [(0.02 + 0.0005 * i, 1) for i in range(15)]
             + [(0.035 + 0.0005 * i, 2) for i in range(15)] + [(0.06 + 0.002 * i, 3) for i in range(15)])
    s = truth.suggest_thresholds(pairs)
    assert 0.043 <= s["tier3_min"]["value"] <= 0.06 and s["tier3_min"]["balanced_accuracy"] >= 0.95
    assert 0.028 <= s["tier2_min"]["value"] <= 0.035
    assert 0.014 <= s["tier1_min"]["value"] <= 0.02
    assert truth.suggest_thresholds([(0.1, 3)] * 5) == {}


def test_metrics_map_to_config_keys():
    from photosort.config import DEFAULTS
    for path, *keys in truth.METRICS.values():
        assert path.startswith("$.primary_") and len(keys) == 3 and all(k in DEFAULTS["focus"] for k in keys)


def test_suggested_cuts_stay_ordered():
    import random
    rng = random.Random(7)
    for _ in range(20):   # small, noisy sets: each tier's best cut on its own can cross the next one's
        pairs = [(rng.uniform(0.001, 0.1), rng.randint(0, 3)) for _ in range(40)]
        s = truth.suggest_thresholds(pairs)
        cuts = [s[k]["value"] for k in ("tier1_min", "tier2_min", "tier3_min") if k in s]
        assert cuts == sorted(cuts)
