#!/usr/bin/env python3
"""Detect faces and output 512-d embeddings for clustering.

Stdout JSON: {"faces":[{"bbox":[x1,y1,x2,y2],"embedding":[...],"score":0.99}],"engine":"insightface"}
bbox values are normalized 0..1 relative to image width/height.
"""
from __future__ import annotations

import argparse
import contextlib
import io
import json
import sys
from pathlib import Path

if hasattr(sys.stdout, "reconfigure"):
    sys.stdout.reconfigure(encoding="utf-8")
else:
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")


def read_image(path: Path):
    """Load BGR image; imdecode handles Unicode paths on Windows."""
    try:
        import cv2
        import numpy as np
    except ImportError:
        return None
    try:
        data = np.fromfile(str(path), dtype=np.uint8)
        if data.size == 0:
            return None
        img = cv2.imdecode(data, cv2.IMREAD_COLOR)
        if img is not None:
            return img
    except OSError:
        pass
    return cv2.imread(str(path))


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", required=True)
    args = parser.parse_args()
    path = Path(args.input)
    if not path.is_file():
        print(json.dumps({"faces": [], "error": "file not found"}, ensure_ascii=False))
        return 1

    try:
        import cv2
        import numpy as np
        from insightface.app import FaceAnalysis
    except ImportError as exc:
        print(json.dumps({"faces": [], "error": f"missing dependency: {exc}"}, ensure_ascii=False))
        return 1

    img = read_image(path)
    if img is None:
        print(json.dumps({"faces": [], "error": "failed to read image"}, ensure_ascii=False))
        return 1

    h, w = img.shape[:2]
    if h <= 0 or w <= 0:
        print(json.dumps({"faces": [], "error": "invalid image size"}, ensure_ascii=False))
        return 1

    with contextlib.redirect_stdout(sys.stderr):
        app = FaceAnalysis(name="buffalo_sc", providers=["CPUExecutionProvider"])
        app.prepare(ctx_id=-1, det_size=(640, 640))

    faces_out = []
    for face in app.get(img):
        bbox = face.bbox.astype(float)
        x1, y1, x2, y2 = bbox.tolist()
        nx1 = max(0.0, min(1.0, x1 / w))
        ny1 = max(0.0, min(1.0, y1 / h))
        nx2 = max(0.0, min(1.0, x2 / w))
        ny2 = max(0.0, min(1.0, y2 / h))
        if nx2 <= nx1 or ny2 <= ny1:
            continue
        emb = face.embedding
        if emb is None:
            continue
        faces_out.append(
            {
                "bbox": [nx1, ny1, nx2, ny2],
                "embedding": np.asarray(emb, dtype=float).tolist(),
                "score": float(getattr(face, "det_score", 0.0) or 0.0),
            }
        )

    print(json.dumps({"faces": faces_out, "engine": "insightface"}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
