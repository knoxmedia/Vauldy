#!/usr/bin/env python3
"""Photo scene classifier: heuristics + optional ONNX (MobileNet/ImageNet).

Outputs JSON to stdout: {"tags": [...], "scores": {...}, "engine": "heuristic|onnx"}
"""
from __future__ import annotations

import argparse
import io
import json
import sys
from pathlib import Path

# Force UTF-8 stdout on Windows so Go receives valid JSON for Chinese tags.
if hasattr(sys.stdout, "reconfigure"):
    sys.stdout.reconfigure(encoding="utf-8")
else:
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")

# ImageNet synset keywords → Chinese scene tags
IMAGENET_KEYWORD_MAP: list[tuple[str, str, float]] = [
    ("golden retriever", "动物", 0.9),
    ("labrador", "动物", 0.9),
    ("tabby", "动物", 0.85),
    ("persian cat", "动物", 0.85),
    ("egyptian cat", "动物", 0.85),
    ("dog", "动物", 0.85),
    ("cat", "动物", 0.8),
    ("bird", "动物", 0.8),
    ("fish", "动物", 0.75),
    ("horse", "动物", 0.8),
    ("cow", "动物", 0.75),
    ("elephant", "动物", 0.8),
    ("bear", "动物", 0.75),
    ("pizza", "美食", 0.9),
    ("ice cream", "美食", 0.85),
    ("cheeseburger", "美食", 0.85),
    ("hotdog", "美食", 0.8),
    ("cake", "美食", 0.85),
    ("burrito", "美食", 0.8),
    ("dining table", "美食", 0.6),
    ("cliff", "风景", 0.85),
    ("valley", "风景", 0.85),
    ("alp", "风景", 0.85),
    ("lakeside", "风景", 0.85),
    ("seashore", "风景", 0.85),
    ("mountain", "风景", 0.8),
    ("volcano", "风景", 0.8),
    ("promontory", "风景", 0.75),
    ("sandbar", "风景", 0.75),
    ("geyser", "风景", 0.7),
    ("church", "建筑", 0.85),
    ("palace", "建筑", 0.85),
    ("library", "建筑", 0.8),
    ("monastery", "建筑", 0.8),
    ("mosque", "建筑", 0.8),
    ("bridge", "建筑", 0.75),
    ("skyscraper", "建筑", 0.8),
    ("apiary", "建筑", 0.5),
    ("person", "人物", 0.85),
    (" groom", "人物", 0.7),
    ("scuba diver", "人物", 0.65),
    ("monitor", "文档/截图", 0.75),
    ("screen", "文档/截图", 0.7),
    ("desktop computer", "文档/截图", 0.7),
    ("notebook", "文档/截图", 0.55),
]


def classify_heuristic_pil(path: Path) -> dict:
    try:
        from PIL import Image
        import colorsys
    except ImportError:
        return {"tags": [], "scores": {}, "engine": "heuristic"}

    img = Image.open(path).convert("RGB")
    img.thumbnail((256, 256))
    pixels = list(img.getdata())
    if not pixels:
        return {"tags": [], "scores": {}, "engine": "heuristic"}

    rs = gs = bs = ss = brs = 0.0
    skin = 0
    for r, g, b in pixels:
        rf, gf, bf = r / 255.0, g / 255.0, b / 255.0
        h, s, v = colorsys.rgb_to_hsv(rf, gf, bf)
        rs += rf
        gs += gf
        bs += bf
        ss += s
        brs += v
        if r > 95 and g > 40 and b > 20 and r > g and r > b and abs(r - g) > 15:
            skin += 1

    n = len(pixels)
    avg_r, avg_g, avg_b = rs / n, gs / n, bs / n
    avg_sat, avg_bright = ss / n, brs / n
    skin_ratio = skin / n

    tags: list[str] = []
    scores: dict[str, float] = {}

    def add(tag: str, score: float) -> None:
        if tag not in scores or score > scores[tag]:
            scores[tag] = score
        if tag not in tags:
            tags.append(tag)

    if avg_sat < 0.12:
        add("黑白", 0.8)
    else:
        if avg_r > avg_b + 0.06:
            add("暖色系", 0.75)
        if avg_b > avg_r + 0.06:
            add("冷色系", 0.75)
        if avg_sat > 0.45:
            add("高饱和度", 0.7)

    if avg_bright < 0.18:
        add("夜景", 0.75)

    w, h = img.size
    ratio = w / max(h, 1)
    if skin_ratio > 0.08:
        add("人物", min(0.55 + skin_ratio, 0.9))
        if ratio < 0.85:
            add("自拍", 0.55)

    if ratio >= 1.2 and avg_g > avg_r and avg_b >= avg_r * 0.85:
        add("风景", 0.55)

    if avg_r > avg_b + 0.05 and avg_sat > 0.35 and 0.25 < avg_bright < 0.75:
        add("美食", 0.45)

    name = path.name.lower()
    if any(k in name for k in ("screenshot", "截图", "screen", "capture")):
        add("文档/截图", 0.85)
        add("手机截图", 0.8)

    return {"tags": tags, "scores": scores, "engine": "heuristic"}


def classify_onnx(path: Path, model_path: Path, labels_path: Path | None) -> dict:
    try:
        import numpy as np
        import onnxruntime as ort
        from PIL import Image
    except ImportError:
        return classify_heuristic_pil(path)

    if not model_path.is_file():
        return classify_heuristic_pil(path)

    labels: list[str] = []
    if labels_path and labels_path.is_file():
        labels = [ln.strip() for ln in labels_path.read_text(encoding="utf-8").splitlines() if ln.strip()]

    img = Image.open(path).convert("RGB").resize((224, 224))
    arr = np.array(img).astype("float32") / 255.0
    mean = np.array([0.485, 0.456, 0.406], dtype=np.float32)
    std = np.array([0.229, 0.224, 0.225], dtype=np.float32)
    arr = (arr - mean) / std
    arr = np.transpose(arr, (2, 0, 1))[None, ...]

    sess = ort.InferenceSession(str(model_path), providers=["CPUExecutionProvider"])
    input_name = sess.get_inputs()[0].name
    logits = sess.run(None, {input_name: arr})[0][0]

    # softmax top-5
    exp = np.exp(logits - np.max(logits))
    probs = exp / exp.sum()
    top_idx = np.argsort(probs)[::-1][:5]

    tags: list[str] = []
    scores: dict[str, float] = {}

    def add(tag: str, score: float) -> None:
        if tag not in scores or score > scores[tag]:
            scores[tag] = score
        if tag not in tags:
            tags.append(tag)

    for idx in top_idx:
        prob = float(probs[idx])
        if prob < 0.05:
            continue
        label = labels[idx] if idx < len(labels) else str(idx)
        label_lower = label.lower()
        matched = False
        for kw, tag, boost in IMAGENET_KEYWORD_MAP:
            if kw in label_lower:
                add(tag, min(prob * boost + 0.2, 0.95))
                matched = True
        if not matched and prob > 0.2:
            # weak fallback from label tokens
            if "dog" in label_lower or "cat" in label_lower:
                add("动物", prob * 0.8)

    base = classify_heuristic_pil(path)
    for t in base["tags"]:
        add(t, base["scores"].get(t, 0.5))

    return {"tags": tags, "scores": scores, "engine": "onnx"}


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--input", required=True)
    ap.add_argument("--engine", default="auto", choices=["auto", "heuristic", "onnx"])
    ap.add_argument("--model", default="")
    ap.add_argument("--labels", default="")
    args = ap.parse_args()

    path = Path(args.input)
    if not path.is_file():
        print(json.dumps({"error": "file not found", "tags": [], "scores": {}, "engine": "none"}))
        return 1

    engine = args.engine
    model = Path(args.model) if args.model else Path()
    labels = Path(args.labels) if args.labels else None

    if engine == "heuristic":
        result = classify_heuristic_pil(path)
    elif engine == "onnx":
        result = classify_onnx(path, model, labels)
    else:
        if model.is_file():
            result = classify_onnx(path, model, labels)
        else:
            result = classify_heuristic_pil(path)

    print(json.dumps(result, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main())
