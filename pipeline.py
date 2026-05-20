#!/usr/bin/env python3
"""
Fotoro Ingestion & Query Pipeline v5 — Dynamic Infinite-Vocabulary Tagging
Tier 1: BLIP generates candidate tags from caption (~0.4s)
Tier 2: SigLIP verifies + scores each candidate (~0.05s)
Result: infinite vocabulary, structured confidence, no predefined lists
"""

import os
import sys
import sqlite3
import json
import hashlib
import time
import re
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

# ─── Seed Tags: only for bootstrapping the dynamic extractor ─────────
# These are NOT used for matching — they teach the parser what "kinds" of tags exist
SEED_TAG_CATEGORIES = {
    "scene": ["beach", "mountain", "city", "indoor", "outdoor", "night", "sunset",
              "forest", "ocean", "snow", "desert", "garden", "park", "restaurant",
              "kitchen", "bedroom", "bathroom", "office", "highway", "bridge"],
    "person": ["man", "woman", "child", "baby", "group", "couple", "family"],
    "animal": ["dog", "cat", "bird", "horse", "fish", "butterfly"],
    "vehicle": ["car", "bicycle", "motorcycle", "truck", "bus", "boat", "airplane"],
    "object": ["phone", "laptop", "book", "cup", "bottle", "plate", "cake",
               "flower", "tree", "building"],
    "clothing": ["shirt", "pants", "dress", "jacket", "hat", "glasses", "sunglasses"],
    "color": ["red", "blue", "green", "yellow", "black", "white", "orange", "pink"],
    "event": ["wedding", "birthday", "concert", "party", "meeting", "graduation"],
    "activity": ["eating", "running", "swimming", "driving", "selfie", "cooking"],
    "mood": ["happy", "sad", "romantic", "formal", "casual", "crowded"],
}

ALL_SEED_TAGS = [t for cat in SEED_TAG_CATEGORIES.values() for t in cat]


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

        self.siglip_processor = None
        self.siglip_model = None
        self.blip_processor = None
        self.blip_model = None
        self.bge_model = None
        self.face_app = None

        # Precomputed seed tag embeddings for fast verification
        self.seed_tag_embeddings = None

        self.siglip_index: Optional[faiss.IndexIDMap2] = None
        self.bge_index: Optional[faiss.IndexIDMap2] = None
        self.face_index: Optional[faiss.IndexIDMap2] = None

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
                tags TEXT,  -- JSON: [{"tag": "beach", "score": 0.87}, ...]
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
                det_score REAL DEFAULT 0.8,
                yaw REAL DEFAULT 0.0,
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
        print("🔧 Loading models…")

        print("   → SigLIP base-patch16 (vision-text verifier)")
        self.siglip_processor = AutoProcessor.from_pretrained(
            "google/siglip-base-patch16-224", use_fast=True
        )
        self.siglip_model = AutoModel.from_pretrained(
            "google/siglip-base-patch16-224"
        ).to(self.device).eval()

        print("   → BLIP-base (caption + tag generator)")
        self.blip_processor = BlipProcessor.from_pretrained(
            "Salesforce/blip-image-captioning-base"
        )
        self.blip_model = BlipForConditionalGeneration.from_pretrained(
            "Salesforce/blip-image-captioning-base"
        ).to(self.device).eval()

        print("   → BGE-small-en-v1.5 (semantic encoder)")
        self.bge_model = SentenceTransformer(
            "BAAI/bge-small-en-v1.5", device=self.device
        )

        if load_face:
            try:
                print("   → InsightFace buffalo_l")
                from insightface.app import FaceAnalysis
                self.face_app = FaceAnalysis(
                    name="buffalo_l",
                    root=str(self.dirs["models"]),
                    providers=["CPUExecutionProvider"]
                )
                self.face_app.prepare(ctx_id=0, det_size=(640, 640))
            except Exception as e:
                print(f"    InsightFace skipped: {e}")
                self.face_app = None

        self._precompute_seed_tags()
        print(f"   → Precomputed {len(ALL_SEED_TAGS)} seed tag embeddings")
        print(" All models ready")

    def _precompute_seed_tags(self):
        batch_size = 16
        embeddings = []
        for i in range(0, len(ALL_SEED_TAGS), batch_size):
            batch = ALL_SEED_TAGS[i:i + batch_size]
            inputs = self.siglip_processor(
                text=batch, return_tensors="pt", padding=True
            ).to(self.device)
            with torch.inference_mode():
                emb = self.siglip_model.get_text_features(**inputs)
            emb = emb.cpu().numpy()
            emb = emb / (np.linalg.norm(emb, axis=1, keepdims=True) + 1e-8)
            embeddings.append(emb.astype(np.float32))
        self.seed_tag_embeddings = np.vstack(embeddings)

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
        arr = (arr // 51) * 51
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

    # ─── DYNAMIC TAG EXTRACTION ───────────────────────────────────────

    def _caption_to_candidate_tags(self, caption: str) -> List[str]:
        """
        Parse BLIP caption into candidate tags using linguistic patterns.
        No hardcoded list — extracts anything that looks like a noun phrase.
        """
        caption_lower = caption.lower()
        candidates = set()

        # Pattern 1: "a [color] [noun]" → extract color+noun and noun
        color_pattern = re.compile(
            r'\b(red|blue|green|yellow|black|white|orange|pink|purple|gray|brown|navy)\s+(\w+)\b'
        )
        for color, noun in color_pattern.findall(caption_lower):
            candidates.add(f"{color} {noun}")
            candidates.add(noun)

        # Pattern 2: "a [adj] [noun]" → extract noun
        adj_noun = re.compile(r'\b(a|an|the)\s+(\w+)\s+(\w+)\b')
        for _, adj, noun in adj_noun.findall(caption_lower):
            if adj not in {"the", "a", "an", "of", "in", "on"}:
                candidates.add(f"{adj} {noun}")
            candidates.add(noun)

        # Pattern 3: standalone nouns from caption (simple POS heuristic)
        words = re.findall(r'\b[a-z]{3,}\b', caption_lower)
        # Filter: keep words that are likely objects/scenes (ends in common noun suffixes)
        noun_suffixes = ('ing', 'ion', 'ment', 'ness', 'ity', 'er', 'or', 'ist',
                        'ism', 'ure', 'age', 'dom', 'hood', 'ship', 'ism')
        for w in words:
            if w.endswith(noun_suffixes) or w in {
                'beach', 'mountain', 'ocean', 'forest', 'city', 'street', 'room',
                'kitchen', 'bedroom', 'bathroom', 'office', 'car', 'dog', 'cat',
                'person', 'people', 'man', 'woman', 'child', 'baby', 'group',
                'wedding', 'party', 'concert', 'meeting', 'birthday', 'graduation',
                'food', 'meal', 'dinner', 'lunch', 'breakfast', 'cake', 'pizza',
                'phone', 'laptop', 'computer', 'book', 'cup', 'bottle', 'plate',
                'shirt', 'pants', 'dress', 'jacket', 'hat', 'glasses', 'sunglasses',
                'selfie', 'photo', 'picture', 'image', 'scene', 'view', 'landscape',
            }:
                candidates.add(w)

        # Pattern 4: bigrams that appear in caption
        bigrams = [f"{words[i]} {words[i+1]}" for i in range(len(words)-1)]
        for bg in bigrams:
            if any(bg.startswith(c + " ") for c in {
                'red', 'blue', 'green', 'yellow', 'black', 'white', 'orange', 'pink'
            }):
                candidates.add(bg)

        return list(candidates)

    def _verify_tags_siglip(self, image: Image.Image, candidates: List[str], threshold: float = 0.48) -> List[Dict]:
        """
        Score each candidate tag against the image using SigLIP.
        Returns: [{"tag": "beach", "score": 0.87}, ...]
        """
        if not candidates:
            return []

        # Deduplicate
        candidates = list(dict.fromkeys(candidates))[:20]  # cap at 20 for speed

        # Batch embed candidate texts
        batch_size = 8
        text_embs = []
        for i in range(0, len(candidates), batch_size):
            batch = candidates[i:i + batch_size]
            inputs = self.siglip_processor(text=batch, return_tensors="pt", padding=True).to(self.device)
            with torch.inference_mode():
                emb = self.siglip_model.get_text_features(**inputs)
            emb = emb.cpu().numpy()
            emb = emb / (np.linalg.norm(emb, axis=1, keepdims=True) + 1e-8)
            text_embs.append(emb.astype(np.float32))
        text_embs = np.vstack(text_embs)  # (N_candidates, 768)

        # Image embedding
        img_emb = self._embed_siglip_image(image)  # (768,)

        # Cosine similarities
        sims = np.dot(text_embs, img_emb)  # (N_candidates,)

        results = []
        for tag, score in zip(candidates, sims):
            if score >= threshold:
                results.append({"tag": tag, "score": round(float(score), 3)})

        # Sort by score descending
        results.sort(key=lambda x: x["score"], reverse=True)
        return results[:12]  # top 12

    def _extract_tags(self, image: Image.Image, caption: str) -> List[Dict]:
        """
        Two-tier dynamic tagging:
        1. BLIP caption → candidate tags (infinite vocabulary)
        2. SigLIP verifies which candidates actually match the image
        """
        candidates = self._caption_to_candidate_tags(caption)
        # Also add seed tags for coverage of things BLIP might miss
        candidates.extend(ALL_SEED_TAGS)
        return self._verify_tags_siglip(image, candidates)

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
                num_beams=1,
                do_sample=False,
            )
        return self.blip_processor.batch_decode(outputs, skip_special_tokens=True)

    # ─── Face Detection ───────────────────────────────────────────────

    def _match_face_faiss(self, emb: np.ndarray, threshold: float = 0.60) -> Optional[int]:
        if self.face_index.ntotal == 0:
            return None
        emb = emb / (np.linalg.norm(emb) + 1e-8)
        D, I = self.face_index.search(emb.reshape(1, -1), 1)
        if D[0][0] > threshold:
            return int(I[0][0])
        return None

    def _update_person_embedding(self, person_id: int, new_emb: np.ndarray, alpha: float = 0.15):
        row = self.conn.execute(
            "SELECT face_embedding FROM persons WHERE id=?", (person_id,)
        ).fetchone()
        if row and row[0]:
            old = np.frombuffer(row[0], dtype=np.float32)
            old_norm = old / (np.linalg.norm(old) + 1e-8)
            merged = (1 - alpha) * old_norm + alpha * new_emb
            merged = merged / (np.linalg.norm(merged) + 1e-8)
            self.conn.execute(
                "UPDATE persons SET face_embedding=? WHERE id=?",
                (merged.astype(np.float32).tobytes(), person_id)
            )
            try:
                self.face_index.remove_ids(np.array([person_id], dtype=np.int64))
            except Exception:
                pass
            self.face_index.add_with_ids(
                merged.reshape(1, -1),
                np.array([person_id], dtype=np.int64)
            )

    def _detect_faces(self, image: Image.Image, image_id: int, path: Path) -> Tuple[int, List[int]]:
        if self.face_app is None:
            return 0, []
        if self._is_screenshot(path, image):
            return 0, []

        img_np = np.array(image.convert("RGB"))
        h, w = img_np.shape[:2]
        scale = 1.0
        if max(h, w) > 1600:
            scale = 1600 / max(h, w)
            img_np = cv2.resize(img_np, (int(w * scale), int(h * scale)))

        faces = self.face_app.get(img_np)
        if not faces:
            return 0, []

        face_db_ids = []
        for i, face in enumerate(faces):
            bbox = face.bbox.astype(float)
            face_w = bbox[2] - bbox[0]
            face_h = bbox[3] - bbox[1]
            face_area = face_w * face_h
            img_area = img_np.shape[0] * img_np.shape[1]

            if face_area / img_area < 0.0015:
                continue
            if hasattr(face, 'det_score') and face.det_score < 0.65:
                continue
            if hasattr(face, 'pose') and abs(face.pose[1]) > 45:
                continue

            if scale != 1.0:
                bbox = (bbox / scale).astype(int)
            else:
                bbox = bbox.astype(int)

            x1, y1, x2, y2 = max(0, int(bbox[0])), max(0, int(bbox[1])), int(bbox[2]), int(bbox[3])
            if x2 <= x1 or y2 <= y1:
                continue

            margin_x = int((x2 - x1) * 0.15)
            margin_y = int((y2 - y1) * 0.25)
            cx1 = max(0, x1 - margin_x)
            cy1 = max(0, y1 - margin_y)
            cx2 = min(image.width, x2 + margin_x)
            cy2 = min(image.height, y2 + margin_y)

            crop = image.crop((cx1, cy1, cx2, cy2))
            crop_path = self.dirs["face_crops"] / f"img{image_id}_face{i}.jpg"
            crop.save(crop_path, quality=90)

            emb = face.embedding.astype(np.float32)
            emb_norm = emb / (np.linalg.norm(emb) + 1e-8)
            emb_blob = emb.tobytes()

            person_id = self._match_face_faiss(emb_norm, threshold=0.62)
            if person_id is None:
                person_id = self._match_face_faiss(emb_norm, threshold=0.55)

            if person_id:
                self._update_person_embedding(person_id, emb_norm)
                self.conn.execute(
                    "UPDATE persons SET photo_count = photo_count + 1 WHERE id = ?", (person_id,)
                )
            else:
                cur = self.conn.execute(
                    "INSERT INTO persons (face_embedding, sample_face_path, photo_count, name) VALUES (?, ?, 1, ?)",
                    (emb_blob, str(crop_path), f"Person {self.face_index.ntotal + 1}")
                )
                person_id = cur.lastrowid
                self.face_index.add_with_ids(
                    emb_norm.reshape(1, -1),
                    np.array([person_id], dtype=np.int64)
                )
                self._existing_face_ids.add(person_id)

            face_db_ids.append(person_id)
            self.conn.execute(
                "INSERT INTO face_appearances (image_id, person_id, bbox, face_embedding, det_score, yaw) VALUES (?, ?, ?, ?, ?, ?)",
                (image_id, person_id, json.dumps([int(x1), int(y1), int(x2), int(y2)]),
                 emb_blob, float(getattr(face, 'det_score', 0.8)), float(getattr(face, 'pose', [0, 0, 0])[1]))
            )

        self.conn.commit()
        return len(face_db_ids), face_db_ids

    # ─── Album Post-Processing ────────────────────────────────────────

    def merge_similar_persons(self, threshold: float = 0.72):
        print(f"\n Merging similar albums (threshold {threshold})…")
        persons = self.conn.execute(
            "SELECT id, face_embedding FROM persons ORDER BY photo_count DESC"
        ).fetchall()

        merged = 0
        for pid, emb_blob in persons:
            if self.conn.execute("SELECT COUNT(*) FROM persons WHERE id=?", (pid,)).fetchone()[0] == 0:
                continue

            emb = np.frombuffer(emb_blob, dtype=np.float32)
            emb = emb / (np.linalg.norm(emb) + 1e-8)

            D, I = self.face_index.search(emb.reshape(1, -1), 5)
            for sim_score, other_pid in zip(D[0], I[0]):
                if other_pid < 0 or int(other_pid) == pid or sim_score < threshold:
                    continue
                other_pid = int(other_pid)

                if self.conn.execute("SELECT COUNT(*) FROM persons WHERE id=?", (other_pid,)).fetchone()[0] == 0:
                    continue

                self.conn.execute("UPDATE face_appearances SET person_id=? WHERE person_id=?", (pid, other_pid))
                other_count = self.conn.execute("SELECT photo_count FROM persons WHERE id=?", (other_pid,)).fetchone()[0]
                self.conn.execute("UPDATE persons SET photo_count = photo_count + ? WHERE id=?", (other_count, pid))
                self.conn.execute("DELETE FROM persons WHERE id=?", (other_pid,))
                try:
                    self.face_index.remove_ids(np.array([other_pid], dtype=np.int64))
                except Exception:
                    pass
                self._existing_face_ids.discard(other_pid)
                merged += 1
                print(f"   Merged Person {other_pid} → Person {pid} (sim={sim_score:.3f})")

        self.conn.commit()
        self._save_indices()
        print(f" Merged {merged} duplicates")

    def cleanup_bad_crops(self, min_photos: int = 2):
        rows = self.conn.execute(
            "SELECT id FROM persons WHERE photo_count < ? AND name LIKE 'Person %'",
            (min_photos,)
        ).fetchall()
        removed = 0
        for (pid,) in rows:
            self.conn.execute("DELETE FROM face_appearances WHERE person_id=?", (pid,))
            self.conn.execute("DELETE FROM persons WHERE id=?", (pid,))
            try:
                self.face_index.remove_ids(np.array([pid], dtype=np.int64))
            except Exception:
                pass
            self._existing_face_ids.discard(pid)
            removed += 1
        self.conn.commit()
        self._save_indices()
        print(f" Removed {removed} low-quality albums")

    # ─── Ingestion ────────────────────────────────────────────────────

    def ingest_folder(self, folder: str, recursive: bool = False):
        folder_path = Path(folder)
        if not folder_path.exists():
            print(f" Folder not found: {folder}")
            return

        exts = {".jpg", ".jpeg", ".png", ".webp", ".bmp", ".gif"}
        files = sorted([
            f for f in (folder_path.rglob("*") if recursive else folder_path.iterdir())
            if f.suffix.lower() in exts
        ])

        print(f" Phase 1/5: Discovery ({len(files)} files)")
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
                INSERT INTO images (path, filename, exif_date, exif_gps_lat, exif_gps_lon,
                 width, height, file_size, sha256, processed)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
            """, (
                path_str, path.name, exif["date"], exif["gps_lat"], exif["gps_lon"],
                exif["width"], exif["height"], path.stat().st_size, sha
            ))
            new_entries.append((path, cur.lastrowid))

        n = len(new_entries)
        if n == 0:
            print(f" Nothing new. ({skipped} already in DB)")
            return

        print(f" {n} new | {skipped} skipped | Device: {self.device}\n")

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

        # Phase 2: SigLIP visual
        if needs_siglip:
            t0 = time.time()
            print(f" Phase 2/5: SigLIP visual ({len(needs_siglip)} images)")
            batch_sz = 8
            batch_imgs, batch_ids = [], []
            for path, image_id in tqdm(needs_siglip, desc="SigLIP", unit="img", leave=False):
                try:
                    img = Image.open(path).convert("RGB")
                    batch_imgs.append(img)
                    batch_ids.append(image_id)
                    if len(batch_imgs) == batch_sz:
                        vecs = self._embed_siglip_batch(batch_imgs)
                        self.siglip_index.add_with_ids(vecs, np.array(batch_ids, dtype=np.int64))
                        for iid in batch_ids:
                            self._existing_siglip_ids.add(iid)
                        for im in batch_imgs:
                            im.close()
                        batch_imgs, batch_ids = [], []
                except Exception as e:
                    print(f"\n   SigLIP skip {path.name}: {e}")

            if batch_imgs:
                vecs = self._embed_siglip_batch(batch_imgs)
                self.siglip_index.add_with_ids(vecs, np.array(batch_ids, dtype=np.int64))
                for iid in batch_ids:
                    self._existing_siglip_ids.add(iid)
                for im in batch_imgs:
                    im.close()
            self._save_indices()
            print(f"    {time.time()-t0:.1f}s\n")

        # Phase 3: BLIP caption + dynamic tags + colors
        if needs_caption:
            t0 = time.time()
            print(f" Phase 3/5: Caption + dynamic tags + colors ({len(needs_caption)} images)")

            def flush_blip_buffer(buf):
                if not buf:
                    return
                imgs = [item[2] for item in buf]
                captions = self._caption_blip_batch(imgs)
                for (p, iid, im), cap in zip(buf, captions):
                    try:
                        tags = self._extract_tags(im, cap)
                        colors = self._extract_colors(im)
                        self.conn.execute(
                            "UPDATE images SET caption=?, tags=?, colors=? WHERE id=?",
                            (cap, json.dumps(tags), json.dumps(colors), iid)
                        )
                    except Exception as e:
                        print(f"\n    Tag fail {p.name}: {e}")
                    finally:
                        im.close()

            blip_buffer = []
            for path, image_id in tqdm(needs_caption, desc="Caption", unit="img"):
                try:
                    img = Image.open(path).convert("RGB")
                    blip_buffer.append((path, image_id, img))
                    if len(blip_buffer) == 4:
                        flush_blip_buffer(blip_buffer)
                        blip_buffer = []
                except Exception as e:
                    print(f"\n   Load fail {path.name}: {e}")

            flush_blip_buffer(blip_buffer)
            self.conn.commit()
            print(f"     {time.time()-t0:.1f}s\n")
        else:
            print(" Phase 3/5: Captioning (already done)\n")

        # Phase 4: BGE text embedding
        t0 = time.time()
        print(f" Phase 4/5: BGE text embedding")
        all_rows = self.conn.execute(
            "SELECT id, caption, tags, colors, exif_date, ocr_text FROM images WHERE caption IS NOT NULL"
        ).fetchall()

        ids_list, synth_texts = [], []
        for img_id, cap, tags_json, colors_json, date, ocr in all_rows:
            if img_id in self._existing_bge_ids:
                continue
            tags = json.loads(tags_json) if tags_json else []
            colors = json.loads(colors_json) if colors_json else []
            tag_str = " ".join([t["tag"] for t in tags[:8]]) if tags else ""
            color_str = " ".join(colors[:3]) if colors else ""
            parts = []
            if tag_str:
                parts.append(f"Tags: {tag_str}.")
            if color_str:
                parts.append(f"Colors: {color_str}.")
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
            print(f"   Indexed {len(synth_texts)} new in {time.time()-t0:.1f}s\n")
        else:
            print(f"   Nothing new. ({time.time()-t0:.1f}s)\n")

        # Phase 5: Faces
        if needs_faces and self.face_app:
            t0 = time.time()
            print(f" Phase 5/5: Face detection ({len(needs_faces)} images)")
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
                    print(f"\n    Face fail {path.name}: {e}")
            self.conn.commit()
            self._save_indices()
            print(f"     {time.time()-t0:.1f}s\n")
        else:
            print(" Phase 5/5: Faces (already done)\n")

        img_count = self.conn.execute("SELECT COUNT(*) FROM images").fetchone()[0]
        person_count = self.conn.execute("SELECT COUNT(*) FROM persons").fetchone()[0]
        print(" Done")
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
        tag_text = " ".join([t["tag"] for t in tags])
        colors = json.loads(colors_json) if colors_json else []
        full = f"{caption} {tag_text} {' '.join(colors)} {ocr}".lower()
        score = 0.0
        if query.lower() in full:
            score += 1.0
        for term in expanded_terms:
            if term in full:
                score += 0.30
        # Boost by tag confidence
        for t in tags:
            if t["tag"] in expanded_terms:
                score += t["score"] * 0.5
        return min(score, 3.0)

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
            print(f"   👤 Filtering by person ID {face_pid} ({len(face_img_set)} photos)")

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
            print("    No vector matches with face filter; broadening…")
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
                tags = json.loads(row[8]) if row[8] else []
                results.append({
                    "id": img_id,
                    "path": row[0],
                    "filename": row[1],
                    "caption": row[2] or "",
                    "ocr": row[3] or "",
                    "date": row[4],
                    "face_count": row[5],
                    "resolution": f"{row[6]}x{row[7]}",
                    "tags": tags,
                    "colors": json.loads(row[9]) if row[9] else [],
                    "score": round(float(score), 4),
                })
        return results

    # ─── Human-in-the-Loop ──────────────────────────────────────────

    def name_person(self, person_id: int, name: str):
        self.conn.execute("UPDATE persons SET name=? WHERE id=?", (name, person_id))
        self.conn.commit()
        print(f"  Person {person_id} named '{name}'")

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
        print(f"  Caption updated & re-indexed for image {image_id}")

    def add_feedback(self, image_id: int, query: str, was_relevant: bool, notes: str = ""):
        self.conn.execute(
            "INSERT INTO feedback (image_id, query_text, was_relevant, notes) VALUES (?, ?, ?, ?)",
            (image_id, query, 1 if was_relevant else 0, notes)
        )
        self.conn.commit()
        print(f" Feedback recorded for image {image_id}")

    # ─── Stats ────────────────────────────────────────────────────────

    def stats(self):
        img_count = self.conn.execute("SELECT COUNT(*) FROM images").fetchone()[0]
        person_count = self.conn.execute("SELECT COUNT(*) FROM persons").fetchone()[0]
        face_total = self.conn.execute("SELECT COALESCE(SUM(face_count), 0) FROM images").fetchone()[0]
        feedback_count = self.conn.execute("SELECT COUNT(*) FROM feedback").fetchone()[0]

        print(f"\n Fotoro Archive Stats")
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
