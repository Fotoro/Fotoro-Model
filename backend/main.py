#!/usr/bin/env python3
"""
Fotoro FastAPI Backend — Wired to existing pipeline.py, cache/fotoro.db, index/*.faiss
Run from project root:  uvicorn backend.main:app --host 0.0.0.0 --port 8080
"""
import sys
import uuid
import io
import json
import mimetypes
from pathlib import Path
from collections import defaultdict
import asyncio

# Ensure pipeline.py is importable from project root
PROJECT_ROOT = Path(__file__).parent.parent.resolve()
sys.path.insert(0, str(PROJECT_ROOT))

from fastapi import FastAPI, Form, HTTPException, Query
from fastapi.staticfiles import StaticFiles
from fastapi.responses import FileResponse, StreamingResponse
from fastapi.middleware.cors import CORSMiddleware
from PIL import Image

from pipeline import FotoroPipeline

app = FastAPI(title="Fotoro", version="1.0.0")
app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_methods=["*"],
    allow_headers=["*"],
)

# --- Connect to existing archive ---
print(f"[Fotoro] Project root: {PROJECT_ROOT}")
pipeline = FotoroPipeline(root_dir=str(PROJECT_ROOT))
print(f"[Fotoro] DB: {pipeline.db_path}  (exists={pipeline.db_path.exists()})")
print(f"[Fotoro] SigLIP index: {pipeline.siglip_index_path.exists()}")
print(f"[Fotoro] BGE index: {pipeline.bge_index_path.exists()}")

# Verify connection immediately
_img_count = pipeline.conn.execute("SELECT COUNT(*) FROM images").fetchone()[0]
_person_count = pipeline.conn.execute("SELECT COUNT(*) FROM persons").fetchone()[0]
print(f"[Fotoro] Archive connected: {_img_count} images, {_person_count} persons")

_models_loaded = False
def ensure_models(load_face: bool = False):
    global _models_loaded
    if not _models_loaded:
        print("[Fotoro] Loading ML models...")
        pipeline.load_models(load_face=load_face)
        _models_loaded = True
        print("[Fotoro] Models ready")

jobs = {}

def _run_ingest(job_id: str, folder: str, recursive: bool, no_faces: bool):
    try:
        jobs[job_id] = {"status": "running", "progress": 10, "message": "Loading models..."}
        ensure_models(load_face=not no_faces)
        jobs[job_id].update({"progress": 30, "message": "Scanning & embedding..."})
        pipeline.ingest_folder(folder, recursive=recursive)
        jobs[job_id] = {"status": "completed", "progress": 100, "message": "Import complete"}
    except Exception as e:
        jobs[job_id] = {"status": "failed", "progress": 0, "message": str(e)}
        print(f"[Fotoro] Ingest failed: {e}")

@app.on_event("startup")
async def startup():
    (PROJECT_ROOT / "backend" / "static").mkdir(parents=True, exist_ok=True)

# ---------- Health / Verify ----------
@app.get("/api/health")
async def health():
    img_count = pipeline.conn.execute("SELECT COUNT(*) FROM images").fetchone()[0]
    return {
        "status": "ok",
        "root": str(PROJECT_ROOT),
        "db_path": str(pipeline.db_path),
        "db_exists": pipeline.db_path.exists(),
        "images_in_db": img_count,
        "models_loaded": _models_loaded,
    }

# ---------- Ingest ----------
@app.post("/api/ingest")
async def ingest(folder: str = Form(...), recursive: bool = Form(False), no_faces: bool = Form(False)):
    job_id = str(uuid.uuid4())
    asyncio.create_task(asyncio.to_thread(_run_ingest, job_id, folder, recursive, no_faces))
    jobs[job_id] = {"status": "queued", "progress": 0, "message": "Queued"}
    return {"job_id": job_id}

@app.get("/api/ingest/{job_id}")
async def ingest_status(job_id: str):
    if job_id not in jobs:
        raise HTTPException(404, "Job not found")
    return jobs[job_id]

# ---------- Search ----------
@app.post("/api/search")
async def search(q: str = Form(...), k: int = Form(20)):
    ensure_models(load_face=False)
    results = pipeline.query(q, top_k=k)
    return {"query": q, "count": len(results), "results": results}

# ---------- Gallery by Date ----------
def _normalize_date(d) -> str:
    if not d:
        return "Unknown"
    s = str(d)
    # EXIF DateTimeOriginal uses colons: "2023:12:25 10:00:00"
    if len(s) >= 10 and s[4] == ':' and s[7] == ':':
        return f"{s[0:4]}-{s[5:7]}-{s[8:10]}"
    if len(s) >= 10:
        return s[0:10]
    return "Unknown"

@app.get("/api/gallery")
async def gallery_by_date(offset: int = Query(0, ge=0), limit: int = Query(50, ge=1, le=200)):
    rows = pipeline.conn.execute(
        """SELECT id, path, filename, caption, width, height,
                  COALESCE(exif_date, created_at) as sort_date,
                  exif_date, face_count
           FROM images
           ORDER BY sort_date DESC, id DESC
           LIMIT ? OFFSET ?""", (limit, offset)
    ).fetchall()

    groups = defaultdict(list)
    for r in rows:
        date_key = _normalize_date(r[6])
        groups[date_key].append({
            "id": r[0], "path": r[1], "filename": r[2], "caption": r[3],
            "width": r[4], "height": r[5], "date": r[7], "face_count": r[8]
        })

    # Newest dates first; "Unknown" always last
    sorted_groups = sorted(groups.items(), key=lambda x: (x[0] == "Unknown", x[0]), reverse=True)
    return {"offset": offset, "limit": limit, "groups": dict(sorted_groups)}

# ---------- Image Serving ----------
@app.get("/api/image/{image_id}")
async def get_image(image_id: int, thumb: bool = False):
    row = pipeline.conn.execute(
        "SELECT path, filename FROM images WHERE id=?", (image_id,)
    ).fetchone()
    if not row:
        raise HTTPException(404, "Image not found")
    path, filename = row[0], row[1]

    if not Path(path).exists():
        # Fallback relative to project root (useful if paths shifted slightly)
        alt = PROJECT_ROOT / Path(path).name
        if alt.exists():
            path = str(alt)
        else:
            raise HTTPException(404, "File missing on disk")

    media_type, _ = mimetypes.guess_type(filename or path)
    if not media_type:
        media_type = "image/jpeg"

    if thumb:
        try:
            img = Image.open(path)
            img.thumbnail((400, 400), Image.Resampling.LANCZOS)
            buf = io.BytesIO()
            fmt = "JPEG" if img.mode in ("RGB", "L") else "PNG"
            img.save(buf, format=fmt, quality=82, optimize=True)
            buf.seek(0)
            return StreamingResponse(buf, media_type="image/jpeg" if fmt == "JPEG" else "image/png")
        except Exception:
            pass  # fall through to original

    return FileResponse(path, media_type=media_type, filename=filename)

# ---------- Metadata ----------
@app.get("/api/images/{image_id}")
async def get_image_meta(image_id: int):
    row = pipeline.conn.execute(
        """SELECT id, path, filename, caption, tags, colors, ocr_text, exif_date,
                  width, height, face_count, face_ids
           FROM images WHERE id=?""", (image_id,)
    ).fetchone()
    if not row:
        raise HTTPException(404, "Image not found")
    return {
        "id": row[0], "path": row[1], "filename": row[2], "caption": row[3],
        "tags": json.loads(row[4]) if row[4] else [],
        "colors": json.loads(row[5]) if row[5] else [],
        "ocr": row[6], "date": row[7], "width": row[8], "height": row[9],
        "face_count": row[10], "face_ids": json.loads(row[11]) if row[11] else []
    }

@app.get("/api/stats")
async def stats():
    c = pipeline.conn
    img_count = c.execute("SELECT COUNT(*) FROM images").fetchone()[0]
    person_count = c.execute("SELECT COUNT(*) FROM persons").fetchone()[0]
    face_total = c.execute("SELECT COALESCE(SUM(face_count),0) FROM images").fetchone()[0]
    return {
        "images": img_count, "persons": person_count, "face_appearances": face_total,
        "visual_vectors": pipeline.siglip_index.ntotal if pipeline.siglip_index else 0,
        "text_vectors": pipeline.bge_index.ntotal if pipeline.bge_index else 0,
        "face_vectors": pipeline.face_index.ntotal if pipeline.face_index else 0,
    }

# ---------- People ----------
@app.get("/api/persons")
async def list_persons():
    rows = pipeline.list_persons()
    return [{"id": r[0], "name": r[1], "photo_count": r[2], "sample_face_path": r[3]} for r in rows]

@app.get("/api/persons/{person_id}/photos")
async def person_photos(person_id: int, limit: int = Query(50, ge=1, le=200)):
    ids = pipeline.get_person_photos(person_id)
    photos = []
    for img_id in ids[:limit]:
        row = pipeline.conn.execute(
            "SELECT id, path, filename, caption FROM images WHERE id=?", (img_id,)
        ).fetchone()
        if row:
            photos.append({"id": row[0], "path": row[1], "filename": row[2], "caption": row[3]})
    return {"person_id": person_id, "count": len(photos), "photos": photos}

@app.post("/api/persons/{person_id}/name")
async def name_person(person_id: int, name: str = Form(...)):
    pipeline.name_person(person_id, name)
    return {"success": True}

@app.get("/api/face/{person_id}")
async def get_face_crop(person_id: int):
    row = pipeline.conn.execute(
        "SELECT sample_face_path FROM persons WHERE id=?", (person_id,)
    ).fetchone()
    if not row or not row[0]:
        raise HTTPException(404, "No face sample")
    path = row[0]
    if not Path(path).exists():
        raise HTTPException(404, "Face crop missing")
    return FileResponse(path)

# ---------- Feedback ----------
@app.post("/api/feedback")
async def feedback(image_id: int = Form(...), query: str = Form(...), relevant: bool = Form(True), note: str = Form("")):
    pipeline.add_feedback(image_id, query, relevant, note)
    return {"success": True}

@app.post("/api/correct-caption")
async def correct_caption(image_id: int = Form(...), caption: str = Form(...)):
    ensure_models(load_face=False)
    pipeline.correct_caption(image_id, caption)
    return {"success": True}

# ---------- Static GUI ----------
static_dir = PROJECT_ROOT / "backend" / "static"
if static_dir.exists():
    app.mount("/", StaticFiles(directory=static_dir, html=True), name="static")

if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=8080)