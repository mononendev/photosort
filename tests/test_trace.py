"""The Trace page's step-by-step walk of the local tier must land where local.local_tier does."""
import copy
import random

from photosort import config, trace as T


def _person(rng, x):
    top = rng.uniform(50, 400)
    h = rng.uniform(150, 600)
    box = [x, top, x + h * 0.4, top + h]
    head = [box[0], box[1], box[0] + h * 0.15, box[1] + h * 0.15]
    p = {"box": box, "head": head, "torso": [box[0], top + h * 0.2, box[2], top + h * 0.6], "conf": rng.uniform(0.2, 1),
         "priority": rng.random(), "sharp_head": rng.choice([None, rng.uniform(0, 0.06)]),
         "sharp_body": rng.uniform(0, 0.05), "sharp_eye": rng.choice([None, rng.uniform(0, 0.1)]),
         "hf_eye": rng.choice([None, rng.uniform(0, 0.05)])}
    if rng.random() < 0.7:
        p["plane"] = {"head": 1.0, "near": 0.8, "torso": 0.9, "head_vs_near": rng.uniform(0, 1), "head_vs_torso": rng.uniform(0, 1)}
    return p


def test_walk_matches_local_tier():
    rng = random.Random(7)
    base = config.DEFAULTS
    for _ in range(3000):
        cfg = copy.deepcopy(base)
        f = cfg["focus"]
        f["floor_tier"] = rng.choice([None, 1, 2, 3])
        f["floor_grade"] = rng.choice([2, 3])
        f["plane_body_max_extra"] = rng.choice([None, 0.5])
        f["plane_max_extra"] = rng.choice([None, 0.4])
        f["use_plane"] = rng.random() < 0.8
        f["use_front"] = rng.random() < 0.8
        f["use_eyes"] = rng.random() < 0.8
        f["use_hf"] = rng.random() < 0.8
        f["front_min_height"] = rng.choice([0.5, 1.0])
        people = [_person(rng, rng.uniform(0, 1500)) for _ in range(rng.randint(0, 4))]
        local = {"width": 2000, "height": 1300, "n_people": len(people), "people": people,
                 "exif_prior": {"motion_risk": rng.choice([None, "low", "high"])}, "local_tier": None, "local_reason": None,
                 "exif": {"iso": rng.choice([None, 400, 6400])}, "exposure": rng.choice([None, {"ev": 2.0}]),
                 "noise": rng.choice([None, {"sigma": rng.uniform(0, 6)}])}
        primary, others = (people[0], people[1:]) if people else (None, [])
        stage, check = T._local_tier(local, cfg, people, primary, others)
        assert check["traced"] == check["engine"], (check, people)
        assert sum(n["decided"] for n in stage["nodes"]) == 1
        # every rule is listed whether or not it was reached
        assert any("soft person" in n["q"] for n in stage["nodes"])
