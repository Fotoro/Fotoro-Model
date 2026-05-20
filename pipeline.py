#!/usr/bin/env python3
"""
Fotoro Ingestion & Query Pipeline v4.1 — Fast Specialist Cascade
BLIP captioning + SigLIP zero-shot tagging + color extraction + hybrid search
"""

import os
import sys
import sqlite3
import json
import hashlib
import time
from pathlib import Path
from typing import List, Dict, Optional, Tuple, Set
import warnings

import numpy as np
import faiss
import torch
import cv2
from PIL import Image, ExifTags
from transformers import AutoProcessor, AutoModel, BlipProcessor, BlipForConditionalGeneration
from sentence_transformers import SentenceTransformer
from tqdm import tqdm

warnings.filterwarnings("ignore")

# ─── Structured Tag Vocabulary (pre-computed once) ───────────────────
TAG_VOCABULARY = [
    # Scenes
    "beach", "mountain", "city street", "indoor room", "outdoor scene", "night scene",
    "sunset", "forest", "ocean view", "snowy landscape", "desert", "garden", "park",
    "restaurant interior", "kitchen", "bedroom", "bathroom", "office", "classroom",
    "stadium", "airport", "highway", "bridge", "river", "lake",
    # People / Animals
    "person", "people", "man", "woman", "child", "baby", "group of people",
    "dog", "cat", "bird", "horse", "fish", "insect", "butterfly", "wild animal",
    # Objects / Vehicles
    "car", "bicycle", "motorcycle", "truck", "bus", "boat", "airplane", "train",
    "phone", "laptop", "computer", "camera", "television", "book", "cup", "bottle",
    "plate of food", "pizza", "burger", "cake", "ice cream", "coffee cup", "wine glass",
    "flower bouquet", "tree", "building", "house",
    # Clothing / Attributes
    "red shirt", "blue shirt", "white shirt", "black shirt", "green shirt", "yellow shirt",
    "orange shirt", "pink shirt", "purple shirt", "gray shirt", "brown shirt",
    "blue jeans", "black pants", "white pants", "red dress", "black dress", "white dress",
    "blue dress", "suit", "jacket", "coat", "hat", "glasses", "sunglasses", "backpack",
    "shirtless person", "bare chest", "topless", "bikini", "swimsuit",
    # Events / Activities
    "wedding", "birthday party", "concert", "sports game", "running", "swimming",
    "eating", "cooking", "hiking", "driving", "selfie", "screenshot", "graduation",
    "meeting", "dance party",
    # Mood / Style
    "happy people", "romantic scene", "formal event", "casual setting", "crowded place",
    "empty room", "dark photo", "bright photo", "colorful scene", "blurry motion",
]


class FotoroPipeline:
    def __init__(self, root_dir: str = "."):
        self.root = Path(root_dir).resolve()
        self.dirs = {
            "cache": self.root / "cache",
            "index": self.root / "index",
            "models": self.root / "models",
            "face_crops": self.root / "face_crops",
            "person_albums": self.root / "person_albums",
        }
        for d in self.dirs.values():
            d.mkdir(exist_ok=True)

        os.environ["TRANSFORMERS_CACHE"] = str(self.dirs["models"])
        os.environ["HF_HOME"] = str(self.dirs["models"])

        cpu_cores = os.cpu_count() or 4
        torch.set_num_threads(min(4, cpu_cores))
        torch.set_num_interop_threads(2)

        self.db_path = self.dirs["cache"] / "fotoro.db"
        self.siglip_index_path = self.dirs["index"] / "siglip.faiss"
        self.bge_index_path = self.dirs["index"] / "bge.faiss"
        self.face_index_path = self.dirs["index"] / "face.faiss"

        self.device = "cuda" if torch.cuda.is_available() else "cpu"
        self.dtype = torch.float32

        # Models
        self.siglip_processor = None
        self.siglip_model = None
        self.blip_processor = None
        self.blip_model = None
        self.bge_model = None
        self.face_app = None

        # Precomputed SigLIP text embeddings for all tags: (N_tags, 768)
        self.tag_embeddings = None

        # FAISS indices
        self.siglip_index: Optional[faiss.IndexIDMap2] = None
        self.bge_index: Optional[faiss.IndexIDMap2] = None
        self.face_index: Optional[faiss.IndexIDMap2] = None

        # Python sets for fast resume checks (populated from FAISS id_map)
        self._existing_siglip_ids: Set[int] = set()
        self._existing_bge_ids: Set[int] = set()
        self._existing_face_ids: Set[int] = set()

        self._init_db()
        self._init_faiss()

    # ─── DB ─────────────────────────────────────────────────────────────

    def _init_db(self):
        self.conn = sqlite3.connect(self.db_path)
        self.conn.execute("""
            CREATE TABLE IF NOT EXISTS images (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                path TEXT UNIQUE NOT NULL,
                filename TEXT,
                caption TEXT,
                tags TEXT,
                colors TEXT,
                ocr_text TEXT,
                exif_date TEXT,
                exif_gps_lat REAL,
                exif_gps_lon REAL,
                width INTEGER,
                height INTEGER,
                file_size INTEGER,
                sha256 TEXT,
                face_count INTEGER DEFAULT 0,
                face_ids TEXT,
                processed INTEGER DEFAULT 0,
                created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
            )
        """)
        self.conn.execute("""
            CREATE TABLE IF NOT EXISTS persons (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                name TEXT DEFAULT 'Unknown',
                face_embedding BLOB,
                sample_face_path TEXT,
                photo_count INTEGER DEFAULT 0,
                created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
            )
        """)
        self.conn.execute("""
            CREATE TABLE IF NOT EXISTS face_appearances (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                image_id INTEGER NOT NULL,
                person_id INTEGER NOT NULL,
                bbox TEXT,
                face_embedding BLOB,
                created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
                FOREIGN KEY(image_id) REFERENCES images(id),
                FOREIGN KEY(person_id) REFERENCES persons(id)
            )
        """)
        self.conn.execute("""
            CREATE TABLE IF NOT EXISTS feedback (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                image_id INTEGER NOT NULL,
                query_text TEXT,
                was_relevant INTEGER DEFAULT 1,
                notes TEXT,
                created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
            )
        """)
        self.conn.commit()

    # ─── FAISS ──────────────────────────────────────────────────────────

    @staticmethod
    def _index_id_set(index: faiss.IndexIDMap2) -> Set[int]:
        """Safely extract all IDs from a FAISS IndexIDMap/IndexIDMap2 into a Python set."""
        if index.ntotal == 0:
            return set()
        try:
            arr = faiss.vector_to_array(index.id_map)
            return set(arr.tolist())
        except Exception:
            return set()

    def _init_faiss(self):
        def load_or_create(path, dim):
            if path.exists():
                return faiss.read_index(str(path))
            return faiss.IndexIDMap2(faiss.IndexFlatIP(dim))

        self.siglip_index = load_or_create(self.siglip_index_path, 768)
        self.bge_index = load_or_create(self.bge_index_path, 384)
        self.face_index = load_or_create(self.face_index_path, 512)

        self._existing_siglip_ids = self._index_id_set(self.siglip_index)
        self._existing_bge_ids = self._index_id_set(self.bge_index)
        self._existing_face_ids = self._index_id_set(self.face_index)

    def _save_indices(self):
        faiss.write_index(self.siglip_index, str(self.siglip_index_path))
        faiss.write_index(self.bge_index, str(self.bge_index_path))
        faiss.write_index(self.face_index, str(self.face_index_path))

    # ─── Model Loading ────────────────────────────────────────────────

    def load_models(self, load_face: bool = True):
        print("->  Loading models…")

        print("   → SigLIP base-patch16 (vision-text encoder + tagger)")
        self.siglip_processor = AutoProcessor.from_pretrained(
            "google/siglip-base-patch16-224", use_fast=True
        )
        self.siglip_model = AutoModel.from_pretrained(
            "google/siglip-base-patch16-224"
        ).to(self.device).eval()

        print("   → BLIP-base (fast captioning ~0.4 s/img on CPU)")
        self.blip_processor = BlipProcessor.from_pretrained(
            "Salesforce/blip-image-captioning-base"
        )
        self.blip_model = BlipForConditionalGeneration.from_pretrained(
            "Salesforce/blip-image-captioning-base"
        ).to(self.device).eval()

        print("   → BGE-small-en-v1.5 (text semantic encoder)")
        self.bge_model = SentenceTransformer(
            "BAAI/bge-small-en-v1.5", device=self.device
        )

        if load_face:
            try:
                print("   → InsightFace buffalo_l (face detect + recognize)")
                from insightface.app import FaceAnalysis
                self.face_app = FaceAnalysis(
                    name="buffalo_l",
                    root=str(self.dirs["models"]),
                    providers=["CPUExecutionProvider"]
                )
                self.face_app.prepare(ctx_id=0, det_size=(640, 640))
            except Exception as e:
                print(f"   ->  InsightFace skipped: {e}")
                self.face_app = None

        # Precompute tag embeddings once (~1 second)
        self._precompute_tag_embeddings()
        print(f"   → Precomputed {len(TAG_VOCABULARY)} tag embeddings")
        print("->  All models ready")

    def _precompute_tag_embeddings(self):
        batch_size = 16
        embeddings = []
        for i in range(0, len(TAG_VOCABULARY), batch_size):
            batch = TAG_VOCABULARY[i:i + batch_size]
            inputs = self.siglip_processor(
                text=batch, return_tensors="pt", padding=True
            ).to(self.device)
            with torch.inference_mode():
                emb = self.siglip_model.get_text_features(**inputs)
            emb = emb.cpu().numpy()
            emb = emb / (np.linalg.norm(emb, axis=1, keepdims=True) + 1e-8)
            embeddings.append(emb.astype(np.float32))
        self.tag_embeddings = np.vstack(embeddings)

    # ─── Helpers ──────────────────────────────────────────────────────

    @staticmethod
    def _is_screenshot(path: Path, image: Image.Image) -> bool:
        name = path.name.lower()
        hints = ["screenshot", "screencap", "snapchat", "snap", "capture", "screen_",
                 "reddit", "twitter", "instagram", "chat", "whatsapp", "com."]
        if any(h in name for h in hints):
            return True
        w, h = image.size
        return max(w, h) / min(w, h) > 2.2

    @staticmethod
    def _extract_exif(img: Image.Image) -> Dict:
        meta = {"date": None, "gps_lat": None, "gps_lon": None, "width": img.width, "height": img.height}
        try:
            exif = img._getexif()
            if not exif:
                return meta
            for tag_id, value in exif.items():
                tag = ExifTags.TAGS.get(tag_id, tag_id)
                if tag == "DateTimeOriginal" and not meta["date"]:
                    meta["date"] = str(value)
                elif tag == "GPSInfo":
                    def dms_to_dd(dms):
                        d, m, s = dms
                        return float(d) + float(m) / 60 + float(s) / 3600
                    gps = value
                    lat_ref, lat_dms = gps.get(1), gps.get(2)
                    lon_ref, lon_dms = gps.get(3), gps.get(4)
                    if lat_dms and lon_dms:
                        lat = dms_to_dd(lat_dms)
                        lon = dms_to_dd(lon_dms)
                        meta["gps_lat"] = -lat if lat_ref == "S" else lat
                        meta["gps_lon"] = -lon if lon_ref == "W" else lon
        except Exception:
            pass
        return meta

    @staticmethod
    def _sha256(path: Path) -> str:
        h = hashlib.sha256()
        with open(path, "rb") as f:
            for chunk in iter(lambda: f.read(65536), b""):
                h.update(chunk)
        return h.hexdigest()

    @staticmethod
    def _rgb_to_name(r: int, g: int, b: int) -> str:
        colors = {
            "red": (255, 0, 0), "green": (0, 255, 0), "blue": (0, 0, 255),
            "yellow": (255, 255, 0), "cyan": (0, 255, 255), "magenta": (255, 0, 255),
            "white": (255, 255, 255), "black": (0, 0, 0), "gray": (128, 128, 128),
            "orange": (255, 165, 0), "purple": (128, 0, 128), "pink": (255, 192, 203),
            "brown": (165, 42, 42), "navy": (0, 0, 128), "teal": (0, 128, 128),
            "lime": (50, 205, 50), "maroon": (128, 0, 0), "olive": (128, 128, 0),
        }
        min_dist = float("inf")
        best = "unknown"
        for name, (cr, cg, cb) in colors.items():
            dist = (r - cr) ** 2 + (g - cg) ** 2 + (b - cb) ** 2
            if dist < min_dist:
                min_dist = dist
                best = name
        return best

    def _extract_colors(self, image: Image.Image, n: int = 3) -> List[str]:
        img = image.resize((30, 30)).convert("RGB")
        arr = np.array(img)
        arr = (arr // 51) * 51  # quantize to 5 levels per channel
        pixels = arr.reshape(-1, 3)
        unique, counts = np.unique(pixels, axis=0, return_counts=True)
        top_idx = np.argsort(counts)[-n:][::-1]
        colors = []
        for idx in top_idx:
            r, g, b = int(unique[idx][0]), int(unique[idx][1]), int(unique[idx][2])
            colors.append(self._rgb_to_name(r, g, b))
        seen = set()
        out = []
        for c in colors:
            if c not in seen:
                seen.add(c)
                out.append(c)
        return out

    def _extract_tags(self, image: Image.Image, top_k: int = 12, threshold: float = 0.52) -> List[str]:
        img_emb = self._embed_siglip_image(image)  # (768,)
        sims = np.dot(self.tag_embeddings, img_emb)  # (N_tags,)
        indices = np.argsort(sims)[::-1]
        tags = []
        for idx in indices:
            if sims[idx] < threshold:
                break
            tags.append(TAG_VOCABULARY[idx])
            if len(tags) >= top_k:
                break
        return tags

    # ─── Batch Embedding ──────────────────────────────────────────────

    def _embed_siglip_batch(self, images: List[Image.Image]) -> np.ndarray:
        inputs = self.siglip_processor(images=images, return_tensors="pt", padding=True).to(self.device)
        with torch.inference_mode():
            feats = self.siglip_model.get_image_features(**inputs)
        vecs = feats.cpu().numpy()
        norms = np.linalg.norm(vecs, axis=1, keepdims=True) + 1e-8
        return (vecs / norms).astype(np.float32)

    def _embed_siglip_image(self, image: Image.Image) -> np.ndarray:
        inputs = self.siglip_processor(images=image, return_tensors="pt").to(self.device)
        with torch.inference_mode():
            feats = self.siglip_model.get_image_features(**inputs)
        vec = feats.cpu().numpy().squeeze()
        return (vec / (np.linalg.norm(vec) + 1e-8)).astype(np.float32)

    def _embed_siglip_text(self, text: str) -> np.ndarray:
        inputs = self.siglip_processor(text=text, return_tensors="pt").to(self.device)
        with torch.inference_mode():
            feats = self.siglip_model.get_text_features(**inputs)
        vec = feats.cpu().numpy().squeeze()
        return (vec / (np.linalg.norm(vec) + 1e-8)).astype(np.float32)

    def _embed_bge_batch(self, texts: List[str]) -> np.ndarray:
        texts = [t or "" for t in texts]
        vecs = self.bge_model.encode(
            texts, normalize_embeddings=True, convert_to_numpy=True,
            batch_size=32, show_progress_bar=False
        )
        return vecs.astype(np.float32)

    def _embed_bge(self, text: str) -> np.ndarray:
        vec = self.bge_model.encode(text, normalize_embeddings=True, convert_to_numpy=True)
        return vec.astype(np.float32)

    # ─── BLIP Batched Captioning ─────────────────────────────────────

    def _caption_blip_batch(self, images: List[Image.Image]) -> List[str]:
        inputs = self.blip_processor(images=images, return_tensors="pt").to(self.device)
        with torch.inference_mode():
            outputs = self.blip_model.generate(
                **inputs,
                max_length=60,
                min_length=5,
                num_beams=1,  # greedy = fast
                do_sample=False,
            )
        return self.blip_processor.batch_decode(outputs, skip_special_tokens=True)

    # ─── Face Detection (FAISS + Junction Table) ────────────────────

    def _match_face_faiss(self, emb: np.ndarray, threshold: float = 0.60) -> Optional[int]:
        if self.face_index.ntotal == 0:
            return None
        emb = emb / (np.linalg.norm(emb) + 1e-8)
        D, I = self.face_index.search(emb.reshape(1, -1), 1)
        if D[0][0] > threshold:
            return int(I[0][0])
        return None

    def _detect_faces(self, image: Image.Image, image_id: int, path: Path) -> Tuple[int, List[int]]:
        if self.face_app is None:
            return 0, []
        if self._is_screenshot(path, image):
            return 0, []

        img_np = np.array(image.convert("RGB"))
        h, w = img_np.shape[:2]
        scale = 1.0
        if max(h, w) > 1280:
            scale = 1280 / max(h, w)
            img_np = cv2.resize(img_np, (int(w * scale), int(h * scale)))

        faces = self.face_app.get(img_np)

        face_db_ids = []
        for i, face in enumerate(faces):
            bbox = face.bbox.astype(int)
            if scale != 1.0:
                bbox = (bbox / scale).astype(int)

            x1, y1, x2, y2 = max(0, int(bbox[0])), max(0, int(bbox[1])), int(bbox[2]), int(bbox[3])
            if x2 <= x1 or y2 <= y1:
                continue

            crop = image.crop((x1, y1, x2, y2))
            crop_path = self.dirs["face_crops"] / f"img{image_id}_face{i}.jpg"
            crop.save(crop_path)

            emb = face.embedding.astype(np.float32)
            emb_norm = emb / (np.linalg.norm(emb) + 1e-8)
            emb_blob = emb.tobytes()

            person_id = self._match_face_faiss(emb_norm, threshold=0.60)
            if person_id:
                self.conn.execute(
                    "UPDATE persons SET photo_count = photo_count + 1 WHERE id = ?", (person_id,)
                )
            else:
                cur = self.conn.execute(
                    "INSERT INTO persons (face_embedding, sample_face_path, photo_count) VALUES (?, ?, 1)",
                    (emb_blob, str(crop_path))
                )
                person_id = cur.lastrowid
                self.face_index.add_with_ids(
                    emb_norm.reshape(1, -1),
                    np.array([person_id], dtype=np.int64)
                )
                self._existing_face_ids.add(person_id)

            face_db_ids.append(person_id)
            self.conn.execute(
                "INSERT INTO face_appearances (image_id, person_id, bbox, face_embedding) VALUES (?, ?, ?, ?)",
                (image_id, person_id, json.dumps([int(x1), int(y1), int(x2), int(y2)]), emb_blob)
            )

        self.conn.commit()
        return len(faces), face_db_ids

    # ─── Ingestion (5 Phases, Resume-Friendly) ───────────────────────

    def ingest_folder(self, folder: str, recursive: bool = False):
        folder_path = Path(folder)
        if not folder_path.exists():
            print(f"->  Folder not found: {folder}")
            return

        exts = {".jpg", ".jpeg", ".png", ".webp", ".bmp", ".gif"}
        files = sorted([
            f for f in (folder_path.rglob("*") if recursive else folder_path.iterdir())
            if f.suffix.lower() in exts
        ])

        # Phase 1: Discovery & Metadata
        print(f"->  Phase 1/5: Discovery ({len(files)} files)")
        new_entries = []
        skipped = 0
        for path in tqdm(files, desc="Metadata", unit="img", leave=False):
            path_str = str(path.resolve())
            row = self.conn.execute("SELECT id FROM images WHERE path = ?", (path_str,)).fetchone()
            if row:
                skipped += 1
                continue
            try:
                img = Image.open(path)
                exif = self._extract_exif(img)
                img.close()
            except Exception:
                continue

            sha = self._sha256(path)
            cur = self.conn.execute("""
                INSERT INTO images
                (path, filename, exif_date, exif_gps_lat, exif_gps_lon,
                 width, height, file_size, sha256, processed)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
            """, (
                path_str, path.name, exif["date"], exif["gps_lat"], exif["gps_lon"],
                exif["width"], exif["height"], path.stat().st_size, sha
            ))
            new_entries.append((path, cur.lastrowid))

        n = len(new_entries)
        if n == 0:
            print(f"->  Nothing new to ingest. ({skipped} already in DB)")
            return

        print(f"->  {n} new images | {skipped} skipped | Device: {self.device}\n")

        # Determine resume state using Python sets (fast, no SWIG issues)
        needs_siglip = [(p, iid) for p, iid in new_entries if iid not in self._existing_siglip_ids]
        needs_caption = []
        needs_faces = []
        for path, image_id in new_entries:
            cap = self.conn.execute("SELECT caption FROM images WHERE id=?", (image_id,)).fetchone()[0]
            if cap is None:
                needs_caption.append((path, image_id))
            fa = self.conn.execute("SELECT COUNT(*) FROM face_appearances WHERE image_id=?", (image_id,)).fetchone()[0]
            if fa == 0:
                needs_faces.append((path, image_id))

        # Phase 2: SigLIP visual embedding (batched)
        if needs_siglip:
            t0 = time.time()
            print(f" ->  Phase 2/5: SigLIP visual embedding ({len(needs_siglip)} images)")
            batch_sz = 8
            batch_imgs, batch_ids = [], []
            for path, image_id in tqdm(needs_siglip, desc="SigLIP", unit="img", leave=False):
                try:
                    img = Image.open(path).convert("RGB")
                    batch_imgs.append(img)
                    batch_ids.append(image_id)
                    if len(batch_imgs) == batch_sz:
                        vecs = self._embed_siglip_batch(batch_imgs)
                        self.siglip_index.add_with_ids(
                            vecs, np.array(batch_ids, dtype=np.int64)
                        )
                        for iid in batch_ids:
                            self._existing_siglip_ids.add(iid)
                        for im in batch_imgs:
                            im.close()
                        batch_imgs, batch_ids = [], []
                except Exception as e:
                    print(f"\n   -> SigLIP skip {path.name}: {e}")

            if batch_imgs:
                vecs = self._embed_siglip_batch(batch_imgs)
                self.siglip_index.add_with_ids(vecs, np.array(batch_ids, dtype=np.int64))
                for iid in batch_ids:
                    self._existing_siglip_ids.add(iid)
                for im in batch_imgs:
                    im.close()
            self._save_indices()
            print(f"   ->  {time.time()-t0:.1f}s\n")

        # Phase 3: BLIP captioning + SigLIP tags + colors (batched BLIP)
        if needs_caption:
            t0 = time.time()
            print(f" -> Phase 3/5: AI captioning + tags + colors ({len(needs_caption)} images)")

            def flush_blip_buffer(buf):
                if not buf:
                    return
                imgs = [item[2] for item in buf]
                captions = self._caption_blip_batch(imgs)
                for (p, iid, im), cap in zip(buf, captions):
                    try:
                        tags = self._extract_tags(im)
                        colors = self._extract_colors(im)
                        self.conn.execute(
                            "UPDATE images SET caption=?, tags=?, colors=? WHERE id=?",
                            (cap, json.dumps(tags), json.dumps(colors), iid)
                        )
                    except Exception as e:
                        print(f"\n   -> Tag/color fail {p.name}: {e}")
                    finally:
                        im.close()

            blip_buffer = []  # list of (path, image_id, image)
            for path, image_id in tqdm(needs_caption, desc="Caption", unit="img"):
                try:
                    img = Image.open(path).convert("RGB")
                    blip_buffer.append((path, image_id, img))
                    if len(blip_buffer) == 4:
                        flush_blip_buffer(blip_buffer)
                        blip_buffer = []
                except Exception as e:
                    print(f"\n   ->  Load fail {path.name}: {e}")

            flush_blip_buffer(blip_buffer)
            self.conn.commit()
            print(f"   ->  {time.time()-t0:.1f}s\n")
        else:
            print("->  Phase 3/5: AI captioning (all already done)\n")

        # Phase 4: BGE text embedding (incremental — only new captions)
        t0 = time.time()
        print(f"->  Phase 4/5: BGE text embedding")
        # Find all images with captions that are NOT yet in the BGE index
        all_rows = self.conn.execute(
            "SELECT id, caption, tags, colors, exif_date, ocr_text FROM images WHERE caption IS NOT NULL"
        ).fetchall()

        ids_list, synth_texts = [], []
        for img_id, cap, tags_json, colors_json, date, ocr in all_rows:
            if img_id in self._existing_bge_ids:
                continue  # already indexed
            tags = json.loads(tags_json) if tags_json else []
            colors = json.loads(colors_json) if colors_json else []
            parts = []
            if tags:
                parts.append("Tags: " + ", ".join(tags[:8]) + ".")
            if colors:
                parts.append("Colors: " + ", ".join(colors[:3]) + ".")
            if cap:
                parts.append(cap)
            if date:
                parts.append(f"Date: {date}.")
            if ocr:
                parts.append(f"Text: {ocr}.")
            synth = " ".join(parts)
            ids_list.append(img_id)
            synth_texts.append(synth)

        if synth_texts:
            chunk = 32
            for i in range(0, len(synth_texts), chunk):
                c_chunk = synth_texts[i:i + chunk]
                id_chunk = np.array(ids_list[i:i + chunk], dtype=np.int64)
                vecs = self._embed_bge_batch(c_chunk)
                self.bge_index.add_with_ids(vecs, id_chunk)
                for iid in ids_list[i:i + chunk]:
                    self._existing_bge_ids.add(iid)
            self._save_indices()
            print(f"   Indexed {len(synth_texts)} new vectors in {time.time()-t0:.1f}s\n")
        else:
            print(f"   Nothing new to index. ({time.time()-t0:.1f}s)\n")

        # Phase 5: Face detection
        if needs_faces and self.face_app:
            t0 = time.time()
            print(f"-> Phase 5/5: Face detection ({len(needs_faces)} images)")
            for path, image_id in tqdm(needs_faces, desc="Faces", unit="img"):
                try:
                    img = Image.open(path).convert("RGB")
                    face_count, face_ids = self._detect_faces(img, image_id, path)
                    img.close()
                    if face_count:
                        self.conn.execute(
                            "UPDATE images SET face_count=?, face_ids=? WHERE id=?",
                            (face_count, json.dumps([int(fid) for fid in face_ids]), image_id)
                        )
                except Exception as e:
                    print(f"\n   ->  Face fail {path.name}: {e}")
            self.conn.commit()
            self._save_indices()
            print(f"   ->  {time.time()-t0:.1f}s\n")
        else:
            print("->  Phase 5/5: Face detection (all already done)\n")

        # Summary
        img_count = self.conn.execute("SELECT COUNT(*) FROM images").fetchone()[0]
        person_count = self.conn.execute("SELECT COUNT(*) FROM persons").fetchone()[0]
        print("->  Ingestion complete")
        print(f"   Images:  {img_count}")
        print(f"   Persons: {person_count}")
        print(f"   Vectors: {self.siglip_index.ntotal} visual | {self.bge_index.ntotal} text | {self.face_index.ntotal} faces")

    # ─── Query Engine ─────────────────────────────────────────────────

    @staticmethod
    def _expand_query(text: str) -> Tuple[List[str], Set[str]]:
        expanded = [text]
        terms = set(text.lower().split())
        text_lower = text.lower()
        for keyword, synonyms in {
            "shirtless": ["shirtless", "bare-chested", "bare chested", "without a shirt",
                         "no shirt", "not wearing a shirt", "topless", "naked torso"],
            "beach": ["beach", "seashore", "coast", "sand", "ocean", "sea", "shore", "waves"],
            "dog": ["dog", "puppy", "canine", "pet", "dogs"],
            "cat": ["cat", "kitten", "feline", "cats"],
            "baby": ["baby", "infant", "toddler", "newborn", "child"],
            "wedding": ["wedding", "marriage", "bride", "groom", "ceremony"],
            "birthday": ["birthday", "cake", "candles", "party", "celebration"],
            "car": ["car", "vehicle", "automobile", "driving"],
            "food": ["food", "meal", "dinner", "lunch", "eating", "restaurant"],
            "night": ["night", "evening", "dark", "nighttime", "neon"],
            "mountain": ["mountain", "mountains", "hill", "hiking", "peak"],
            "smile": ["smile", "smiling", "grin", "laughing", "happy", "joy"],
        }.items():
            if keyword in text_lower:
                for syn in synonyms:
                    if syn != keyword:
                        expanded.append(text_lower.replace(keyword, syn))
                terms.update(s.lower() for s in synonyms)
        return expanded, terms

    def _keyword_boost(self, caption: str, tags_json: str, colors_json: str, ocr: str, query: str, expanded_terms: Set[str]) -> float:
        tags = json.loads(tags_json) if tags_json else []
        colors = json.loads(colors_json) if colors_json else []
        full = f"{caption} {' '.join(tags)} {' '.join(colors)} {ocr}".lower()
        score = 0.0
        if query.lower() in full:
            score += 1.0
        for term in expanded_terms:
            if term in full:
                score += 0.30
        return min(score, 2.5)

    def _extract_face_filter(self, text: str) -> Optional[int]:
        rows = self.conn.execute(
            "SELECT id, name FROM persons WHERE name != 'Unknown'"
        ).fetchall()
        text_lower = text.lower()
        for pid, name in rows:
            if name.lower() in text_lower:
                return pid
        return None

    def query(self, text: str, top_k: int = 10) -> List[Dict]:
        if self.siglip_model is None or self.bge_model is None:
            raise RuntimeError("Models not loaded. Call load_models() first.")

        expanded_queries, expanded_terms = self._expand_query(text)
        face_pid = self._extract_face_filter(text)

        face_img_set: Optional[Set[int]] = None
        if face_pid:
            rows = self.conn.execute(
                "SELECT DISTINCT image_id FROM face_appearances WHERE person_id=?",
                (face_pid,)
            ).fetchall()
            face_img_set = {r[0] for r in rows}
            print(f"   -> Filtering by person ID {face_pid} ({len(face_img_set)} photos)")

        rrf_scores: Dict[int, float] = {}

        def search_rrf(q_text: str, weight: float = 1.0):
            q_img = self._embed_siglip_text(q_text).reshape(1, -1)
            q_txt = self._embed_bge(q_text).reshape(1, -1)

            scores_sig, ids_sig = self.siglip_index.search(q_img, top_k * 4)
            scores_bge, ids_bge = self.bge_index.search(q_txt, top_k * 4)

            for rank, img_id in enumerate(ids_sig[0]):
                if img_id < 0:
                    continue
                img_id = int(img_id)
                if face_img_set is not None and img_id not in face_img_set:
                    continue
                rrf_scores[img_id] = rrf_scores.get(img_id, 0.0) + weight * (1.0 / (rank + 60))

            for rank, img_id in enumerate(ids_bge[0]):
                if img_id < 0:
                    continue
                img_id = int(img_id)
                if face_img_set is not None and img_id not in face_img_set:
                    continue
                rrf_scores[img_id] = rrf_scores.get(img_id, 0.0) + weight * (1.0 / (rank + 60))

        search_rrf(expanded_queries[0], weight=1.5)
        for eq in expanded_queries[1:]:
            search_rrf(eq, weight=0.8)

        if not rrf_scores and face_img_set is not None:
            print("   ->  No vector matches with face filter; broadening…")
            face_img_set = None
            search_rrf(expanded_queries[0], weight=1.5)

        final_scores: Dict[int, float] = {}
        candidate_ids = list(rrf_scores.keys())

        like_q = f"%{text}%"
        db_hits = self.conn.execute(
            "SELECT id, caption, tags, colors, ocr_text FROM images WHERE caption LIKE ? OR ocr_text LIKE ?",
            (like_q, like_q)
        ).fetchall()
        for img_id, cap, tags, colors, ocr in db_hits:
            if face_img_set is not None and img_id not in face_img_set:
                continue
            if img_id not in candidate_ids:
                candidate_ids.append(img_id)

        for img_id in candidate_ids:
            row = self.conn.execute(
                "SELECT caption, tags, colors, ocr_text FROM images WHERE id=?", (img_id,)
            ).fetchone()
            if row:
                boost = self._keyword_boost(row[0], row[1], row[2], row[3] or "", text, expanded_terms)
                base = rrf_scores.get(img_id, 0.0)
                final_scores[img_id] = base + boost

        ranked = sorted(final_scores.items(), key=lambda x: x[1], reverse=True)[:top_k]

        results = []
        for img_id, score in ranked:
            row = self.conn.execute("""
                SELECT path, filename, caption, ocr_text, exif_date, face_count, width, height, tags, colors
                FROM images WHERE id = ?
            """, (img_id,)).fetchone()
            if row:
                results.append({
                    "id": img_id,
                    "path": row[0],
                    "filename": row[1],
                    "caption": row[2] or "",
                    "ocr": row[3] or "",
                    "date": row[4],
                    "face_count": row[5],
                    "resolution": f"{row[6]}x{row[7]}",
                    "tags": json.loads(row[8]) if row[8] else [],
                    "colors": json.loads(row[9]) if row[9] else [],
                    "score": round(float(score), 4),
                })
        return results

    # ─── Human-in-the-Loop ──────────────────────────────────────────

    def name_person(self, person_id: int, name: str):
        self.conn.execute("UPDATE persons SET name=? WHERE id=?", (name, person_id))
        self.conn.commit()
        print(f"->  Person {person_id} named '{name}'")

    def correct_caption(self, image_id: int, new_caption: str):
        self.conn.execute("UPDATE images SET caption=? WHERE id=?", (new_caption, image_id))
        self.conn.commit()
        vec = self._embed_bge(new_caption).reshape(1, -1)
        try:
            self.bge_index.remove_ids(np.array([image_id], dtype=np.int64))
        except Exception:
            pass
        self.bge_index.add_with_ids(vec, np.array([image_id], dtype=np.int64))
        self._save_indices()
        print(f"->  Caption updated & re-indexed for image {image_id}")

    def add_feedback(self, image_id: int, query: str, was_relevant: bool, notes: str = ""):
        self.conn.execute(
            "INSERT INTO feedback (image_id, query_text, was_relevant, notes) VALUES (?, ?, ?, ?)",
            (image_id, query, 1 if was_relevant else 0, notes)
        )
        self.conn.commit()
        print(f"-> Feedback recorded for image {image_id}")

    # ─── Stats ────────────────────────────────────────────────────────

    def stats(self):
        img_count = self.conn.execute("SELECT COUNT(*) FROM images").fetchone()[0]
        person_count = self.conn.execute("SELECT COUNT(*) FROM persons").fetchone()[0]
        face_total = self.conn.execute("SELECT COALESCE(SUM(face_count), 0) FROM images").fetchone()[0]
        feedback_count = self.conn.execute("SELECT COUNT(*) FROM feedback").fetchone()[0]

        print(f"\n-> Fotoro Archive Stats")
        print(f"   Images:      {img_count}")
        print(f"   Vectors:     {self.siglip_index.ntotal} visual | {self.bge_index.ntotal} text")
        print(f"   Persons:     {person_count} ({face_total} face appearances)")
        print(f"   Feedback:    {feedback_count} entries")
        if self.db_path.exists():
            print(f"   DB size:     {self.db_path.stat().st_size / 1024:.1f} KB")

    def list_persons(self) -> List[Tuple]:
        return self.conn.execute(
            "SELECT id, name, photo_count, sample_face_path FROM persons ORDER BY photo_count DESC"
        ).fetchall()

    def get_person_photos(self, person_id: int) -> List[int]:
        rows = self.conn.execute(
            "SELECT DISTINCT image_id FROM face_appearances WHERE person_id=? ORDER BY image_id DESC",
            (person_id,)
        ).fetchall()
        return [r[0] for r in rows]
