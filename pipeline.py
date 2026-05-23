#!/usr/bin/env python3
"""
Fotoro Ingestion & Query Pipeline v7.2 — Latency-First Architecture
Targets: 0.05-0.15 s/img on CPU for 500K photo libraries

WIPE PREVIOUS DATA:
    rm -f cache/fotoro.db index/*.faiss && rm -rf face_crops/* person_albums/*

- SigLIP zero-shot tags (no BLIP for 90% of images)
- BLIP only for low-confidence images, greedy decode, batch 32
- Batch 64 for SigLIP, resize 384 (native + margin)
- Face detection: 1 worker, 640px, skip if no person tags
"""

import os
import sys
import sqlite3
import json
import hashlib
import time
import re
import gc
from pathlib import Path
from typing import List, Dict, Optional, Tuple, Set
from concurrent.futures import ProcessPoolExecutor, as_completed
from multiprocessing import cpu_count
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

# --- Expanded Tag Vocabulary (235 tags for zero-shot) ------
TAG_VOCABULARY = [
    "beach", "mountain", "city", "indoor", "outdoor", "night", "sunset", "sunrise",
    "forest", "ocean", "snow", "desert", "garden", "park", "restaurant", "cafe",
    "kitchen", "bedroom", "bathroom", "office", "highway", "bridge", "street", "alley",
    "mall", "store", "library", "classroom", "hospital", "airport", "train station",
    "stadium", "theater", "museum", "church", "temple", "mosque", "castle", "ruins",
    "camping", "hiking trail", "lake", "river", "waterfall", "canyon", "island",
    "man", "woman", "child", "baby", "group of people", "couple", "family", "crowd",
    "selfie", "portrait", "wedding", "bride", "groom", "graduation", "birthday party",
    "concert", "festival", "protest", "parade", "meeting", "presentation", "interview",
    "athlete", "dancer", "musician", "chef", "doctor", "police", "soldier",
    "dog", "puppy", "cat", "kitten", "bird", "horse", "fish", "butterfly", "bee",
    "spider", "snake", "elephant", "lion", "tiger", "bear", "deer", "rabbit",
    "car", "sports car", "bicycle", "motorcycle", "truck", "bus", "boat", "ship",
    "airplane", "helicopter", "train", "subway", "taxi", "ambulance", "fire truck",
    "phone", "smartphone", "laptop", "computer", "tablet", "book", "newspaper",
    "cup", "coffee cup", "mug", "bottle", "wine bottle", "plate", "bowl", "cake",
    "pizza", "burger", "sushi", "salad", "sandwich", "flower", "bouquet", "tree",
    "palm tree", "christmas tree", "building", "skyscraper", "house", "tent", "umbrella",
    "balloon", "kite", "flag", "sign", "poster", "billboard", "statue", "fountain",
    "shirt", "t-shirt", "pants", "jeans", "dress", "skirt", "jacket", "coat", "suit",
    "hat", "cap", "glasses", "sunglasses", "watch", "necklace", "ring", "backpack",
    "handbag", "suitcase", "luggage",
    "red", "blue", "green", "yellow", "black", "white", "orange", "pink", "purple",
    "brown", "gray", "gold", "silver",
    "eating", "drinking", "cooking", "running", "walking", "swimming", "driving",
    "riding", "flying", "dancing", "singing", "playing music", "reading", "writing",
    "painting", "photographing", "shopping", "working", "studying", "sleeping",
    "talking", "laughing", "crying", "kissing", "hugging", "fighting", "exercising",
    "happy", "sad", "romantic", "formal", "casual", "professional", "vintage",
    "blurry", "overexposed", "underexposed", "noisy", "grainy", "sharp", "colorful",
    "food", "meal", "breakfast", "lunch", "dinner", "dessert", "fruit", "vegetable",
    "meat", "seafood", "pasta", "rice", "bread", "cheese", "chocolate", "ice cream",
]

ALL_TAGS = list(dict.fromkeys(TAG_VOCABULARY))


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

        self._n_workers = max(1, (cpu_count() or 4) - 1)
        torch.set_num_threads(2)
        torch.set_num_interop_threads(1)

        self.db_path = self.dirs["cache"] / "fotoro.db"
        self.siglip_index_path = self.dirs["index"] / "siglip.faiss"
        self.bge_index_path = self.dirs["index"] / "bge.faiss"
        self.face_index_path = self.dirs["index"] / "face.faiss"

        self.device = "cuda" if torch.cuda.is_available() else "cpu"
        self.dtype = torch.float32

        self.siglip_processor = None
        self.siglip_model = None
        self._siglip_onnx_session = None
        self.blip_processor = None
        self.blip_model = None
        self.bge_model = None
        self.face_app = None

        self._tag_embeddings: Optional[np.ndarray] = None
        self._tag_list: List[str] = []

        self.siglip_index: Optional[faiss.IndexIDMap2] = None
        self.bge_index: Optional[faiss.IndexIDMap2] = None
        self.face_index: Optional[faiss.IndexIDMap2] = None

        self._existing_siglip_ids: Set[int] = set()
        self._existing_bge_ids: Set[int] = set()
        self._existing_face_ids: Set[int] = set()

        self._init_db()
        self._init_faiss()

    # --- DB ------------------------------------------------------------

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
                tag_confidence REAL DEFAULT 0.0,
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

    # --- FAISS ---------------------------------------------------------

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

    # --- Model Loading -------------------------------------------------

    def load_models(self, load_face: bool = True):
        print("Loading models...")

        self._load_siglip()
        self._load_blip()
        self._load_bge()
        self._precompute_tag_embeddings()

        if load_face:
            try:
                print("   -> InsightFace buffalo_l")
                from insightface.app import FaceAnalysis
                self.face_app = FaceAnalysis(
                    name="buffalo_l",
                    root=str(self.dirs["models"]),
                    providers=["CPUExecutionProvider"]
                )
                self.face_app.prepare(ctx_id=0, det_size=(640, 640))
            except Exception as e:
                print(f"   WARNING: InsightFace skipped: {e}")
                self.face_app = None

        print("All models ready")

    def _load_siglip(self):
        print("   -> SigLIP PyTorch")
        self.siglip_processor = AutoProcessor.from_pretrained(
            "google/siglip-base-patch16-224", use_fast=True
        )
        self.siglip_model = AutoModel.from_pretrained(
            "google/siglip-base-patch16-224"
        ).to(self.device).eval()

        onnx_path = self.dirs["models"] / "siglip_onnx"
        if not onnx_path.exists():
            return

        try:
            import onnxruntime as ort
            sess_options = ort.SessionOptions()
            sess_options.intra_op_num_threads = 2
            sess_options.inter_op_num_threads = 1
            session = ort.InferenceSession(
                str(onnx_path / "model.onnx"),
                sess_options,
                providers=["CPUExecutionProvider"]
            )

            # Smoke test
            test_img = Image.new("RGB", (224, 224), color=(128, 128, 128))
            inputs = self.siglip_processor(images=test_img, return_tensors="pt")
            pv = inputs["pixel_values"].cpu().numpy().astype(np.float32)
            onnx_inputs = {inp.name: inp for inp in session.get_inputs()}
            feed = {"pixel_values": pv}

            # Handle dynamic shapes like 'sequence_length', 'batch_size'
            def _resolve_dim(dim, default=64):
                if isinstance(dim, int):
                    return dim
                if isinstance(dim, str):
                    # Try to parse as int, fallback to default
                    try:
                        return int(dim)
                    except ValueError:
                        return default
                return default

            if "input_ids" in onnx_inputs:
                inp = onnx_inputs["input_ids"]
                seq_len = _resolve_dim(inp.shape[1] if len(inp.shape) > 1 else 64, 64)
                feed["input_ids"] = np.zeros((1, seq_len), dtype=np.int64)
            if "attention_mask" in onnx_inputs:
                seq_len = feed.get("input_ids", np.zeros((1, 64))).shape[1]
                feed["attention_mask"] = np.ones((1, seq_len), dtype=np.int64)

            outputs = session.run(None, feed)
            test_vec = outputs[0].squeeze()
            test_vec = test_vec / (np.linalg.norm(test_vec) + 1e-8)

            with torch.inference_mode():
                pt_vec = self.siglip_model.get_image_features(**inputs).cpu().numpy().squeeze()
            pt_vec = pt_vec / (np.linalg.norm(pt_vec) + 1e-8)
            cos_sim = np.dot(test_vec, pt_vec)

            if cos_sim >= 0.95 and test_vec.shape[0] == 768:
                self._siglip_onnx_session = session
                print(f"   -> SigLIP ONNX active (cos={cos_sim:.3f})")
            else:
                print(f"   -> ONNX mismatch (cos={cos_sim:.3f}), using PyTorch")
        except Exception as e:
            print(f"   -> ONNX failed: {e}")

    def _load_blip(self):
        print("   -> BLIP-base (slow path only)")
        self.blip_processor = BlipProcessor.from_pretrained(
            "Salesforce/blip-image-captioning-base"
        )
        self.blip_model = BlipForConditionalGeneration.from_pretrained(
            "Salesforce/blip-image-captioning-base"
        ).to(self.device).eval()

    def _load_bge(self):
        print("   -> BGE-small-en-v1.5")
        self.bge_model = SentenceTransformer(
            "BAAI/bge-small-en-v1.5", device=self.device
        )

    def _precompute_tag_embeddings(self):
        print(f"   -> Precomputing {len(ALL_TAGS)} tag embeddings...")
        batch_size = 32
        embeddings = []
        for i in range(0, len(ALL_TAGS), batch_size):
            batch = ALL_TAGS[i:i + batch_size]
            embs = []
            for tag in batch:
                inputs = self.siglip_processor(text=tag, return_tensors="pt").to(self.device)
                with torch.inference_mode():
                    feat = self.siglip_model.get_text_features(**inputs)
                vec = feat.cpu().numpy().squeeze()
                vec = vec / (np.linalg.norm(vec) + 1e-8)
                embs.append(vec)
            embeddings.extend(embs)
        self._tag_embeddings = np.vstack(embeddings).astype(np.float32)
        self._tag_list = ALL_TAGS
        print(f"   -> Tag matrix: {self._tag_embeddings.shape}")

    # --- FAST SigLIP Embedding -----------------------------------------

    def _embed_siglip_image(self, image: Image.Image) -> np.ndarray:
        if self._siglip_onnx_session:
            inputs = self.siglip_processor(images=image, return_tensors="pt")
            pv = inputs["pixel_values"].cpu().numpy().astype(np.float32)
            onnx_inputs = {inp.name: inp for inp in self._siglip_onnx_session.get_inputs()}
            feed = {"pixel_values": pv}

            def _resolve_dim(dim, default=64):
                if isinstance(dim, int):
                    return dim
                try:
                    return int(dim)
                except (ValueError, TypeError):
                    return default

            if "input_ids" in onnx_inputs:
                inp = onnx_inputs["input_ids"]
                seq_len = _resolve_dim(inp.shape[1] if len(inp.shape) > 1 else 64, 64)
                feed["input_ids"] = np.zeros((pv.shape[0], seq_len), dtype=np.int64)
            if "attention_mask" in onnx_inputs:
                seq_len = feed.get("input_ids", np.zeros((pv.shape[0], 64))).shape[1]
                feed["attention_mask"] = np.ones((pv.shape[0], seq_len), dtype=np.int64)

            outputs = self._siglip_onnx_session.run(None, feed)
            vec = outputs[0].squeeze()
            return (vec / (np.linalg.norm(vec) + 1e-8)).astype(np.float32)
        else:
            inputs = self.siglip_processor(images=image, return_tensors="pt").to(self.device)
            with torch.inference_mode():
                feats = self.siglip_model.get_image_features(**inputs)
            vec = feats.cpu().numpy().squeeze()
            return (vec / (np.linalg.norm(vec) + 1e-8)).astype(np.float32)

    def _embed_siglip_batch(self, images: List[Image.Image]) -> np.ndarray:
        if self._siglip_onnx_session:
            inputs = self.siglip_processor(images=images, return_tensors="pt")
            pv = inputs["pixel_values"].cpu().numpy().astype(np.float32)
            onnx_inputs = {inp.name: inp for inp in self._siglip_onnx_session.get_inputs()}
            feed = {"pixel_values": pv}

            def _resolve_dim(dim, default=64):
                if isinstance(dim, int):
                    return dim
                try:
                    return int(dim)
                except (ValueError, TypeError):
                    return default

            if "input_ids" in onnx_inputs:
                inp = onnx_inputs["input_ids"]
                seq_len = _resolve_dim(inp.shape[1] if len(inp.shape) > 1 else 64, 64)
                feed["input_ids"] = np.zeros((pv.shape[0], seq_len), dtype=np.int64)
            if "attention_mask" in onnx_inputs:
                seq_len = feed.get("input_ids", np.zeros((pv.shape[0], 64))).shape[1]
                feed["attention_mask"] = np.ones((pv.shape[0], seq_len), dtype=np.int64)

            outputs = self._siglip_onnx_session.run(None, feed)
            vecs = outputs[0]
            if vecs.ndim == 1:
                vecs = vecs.reshape(1, -1)
            norms = np.linalg.norm(vecs, axis=1, keepdims=True) + 1e-8
            return (vecs / norms).astype(np.float32)
        else:
            inputs = self.siglip_processor(images=images, return_tensors="pt", padding=True).to(self.device)
            with torch.inference_mode():
                feats = self.siglip_model.get_image_features(**inputs)
            vecs = feats.cpu().numpy()
            norms = np.linalg.norm(vecs, axis=1, keepdims=True) + 1e-8
            return (vecs / norms).astype(np.float32)

    def _embed_siglip_text(self, text: str) -> np.ndarray:
        inputs = self.siglip_processor(text=text, return_tensors="pt").to(self.device)
        with torch.inference_mode():
            feats = self.siglip_model.get_text_features(**inputs)
        vec = feats.cpu().numpy().squeeze()
        return (vec / (np.linalg.norm(vec) + 1e-8)).astype(np.float32)

    # --- Zero-Shot Tag Classification (FAST) -------------------------

    def _classify_tags_fast(self, image: Image.Image, top_k: int = 10, threshold: float = 0.30) -> Tuple[List[Dict], float]:
        img_emb = self._embed_siglip_image(image)
        sims = np.dot(self._tag_embeddings, img_emb)
        top_idx = np.argsort(sims)[-top_k:][::-1]
        tags = []
        for idx in top_idx:
            if sims[idx] >= threshold:
                tags.append({"tag": self._tag_list[idx], "score": round(float(sims[idx]), 3)})
        max_conf = float(sims.max()) if len(sims) > 0 else 0.0
        return tags, max_conf

    def _tags_to_caption(self, tags: List[Dict], colors: List[str]) -> str:
        if not tags:
            return "a photo"
        top = [t["tag"] for t in tags[:5]]
        parts = []
        scenes = {"beach", "mountain", "city", "forest", "ocean", "desert", "garden", "park",
                  "kitchen", "bedroom", "office", "restaurant", "street", "highway", "lake", "river"}
        scene_tags = [t for t in top if t in scenes]
        if scene_tags:
            parts.append(scene_tags[0])
        beings = [t for t in top if t in {"man", "woman", "child", "baby", "dog", "cat", "bird",
                 "group of people", "couple", "family", "puppy", "kitten"}]
        if beings:
            parts.append("with " + beings[0])
        acts = [t for t in top if t in {"eating", "running", "swimming", "driving", "selfie",
               "walking", "playing music", "cooking", "reading", "dancing"}]
        if acts:
            parts.append(acts[0])
        if colors:
            parts.append(f"dominant {colors[0]} tones")
        caption = ", ".join(parts) if len(parts) > 1 else (parts[0] if parts else "a photo")
        return caption

    # --- BLIP Slow Path ----------------------------------------------

    def _caption_blip_batch(self, images: List[Image.Image]) -> List[str]:
        inputs = self.blip_processor(images=images, return_tensors="pt").to(self.device)
        with torch.inference_mode():
            outputs = self.blip_model.generate(
                **inputs,
                max_length=20,
                min_length=5,
                num_beams=1,
                do_sample=False,
            )
        return self.blip_processor.batch_decode(outputs, skip_special_tokens=True)

    # --- BGE ---------------------------------------------------------

    def _embed_bge_batch(self, texts: List[str]) -> np.ndarray:
        texts = [t or "" for t in texts]
        vecs = self.bge_model.encode(
            texts, normalize_embeddings=True, convert_to_numpy=True,
            batch_size=64, show_progress_bar=False
        )
        return vecs.astype(np.float32)

    def _embed_bge(self, text: str) -> np.ndarray:
        vec = self.bge_model.encode(text, normalize_embeddings=True, convert_to_numpy=True)
        return vec.astype(np.float32)

    # --- Helpers -----------------------------------------------------

    @staticmethod
    def _open_resized(path: Path, max_dim: int = 384) -> Image.Image:
        img = Image.open(path).convert("RGB")
        w, h = img.size
        if max(w, h) > max_dim:
            scale = max_dim / max(w, h)
            img = img.resize((int(w * scale), int(h * scale)), Image.Resampling.LANCZOS)
        return img

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
    def _extract_colors(image: Image.Image, n: int = 3) -> List[str]:
        img = image.resize((30, 30)).convert("RGB")
        arr = np.array(img)
        arr = (arr // 51) * 51
        pixels = arr.reshape(-1, 3)
        unique, counts = np.unique(pixels, axis=0, return_counts=True)
        top_idx = np.argsort(counts)[-n:][::-1]
        colors = []
        for idx in top_idx:
            r, g, b = int(unique[idx][0]), int(unique[idx][1]), int(unique[idx][2])
            if r > 200 and g < 100 and b < 100: colors.append("red")
            elif r < 100 and g > 200 and b < 100: colors.append("green")
            elif r < 100 and g < 100 and b > 200: colors.append("blue")
            elif r > 200 and g > 200 and b < 100: colors.append("yellow")
            elif r > 200 and g > 100 and b < 100: colors.append("orange")
            elif r > 200 and g < 200 and b > 200: colors.append("pink")
            elif r > 200 and g > 200 and b > 200: colors.append("white")
            elif r < 50 and g < 50 and b < 50: colors.append("black")
            elif r > 150 and g > 150 and b > 150: colors.append("gray")
            else: colors.append("mixed")
        seen = set()
        out = []
        for c in colors:
            if c not in seen:
                seen.add(c)
                out.append(c)
        return out

    # --- Face Detection ------------------------------------------------

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

    # --- INGESTION (v7.2) -------------------------------------------

    def ingest_folder(self, folder: str, recursive: bool = False):
        folder_path = Path(folder)
        if not folder_path.exists():
            print(f"ERROR: Folder not found: {folder}")
            return

        exts = {".jpg", ".jpeg", ".png", ".webp", ".bmp", ".gif"}
        files = sorted([
            f for f in (folder_path.rglob("*") if recursive else folder_path.iterdir())
            if f.suffix.lower() in exts
        ])

        print(f"Phase 1/4: Discovery ({len(files)} files)")
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
            print(f"Nothing new. ({skipped} already in DB)")
            return

        print(f"{n} new | {skipped} skipped | Device: {self.device}")

        needs_siglip = []
        needs_faces = []
        for path, iid in new_entries:
            if iid not in self._existing_siglip_ids:
                needs_siglip.append((path, iid))
            fa = self.conn.execute("SELECT COUNT(*) FROM face_appearances WHERE image_id=?", (iid,)).fetchone()[0]
            if fa == 0:
                needs_faces.append((path, iid))

        print(f"   -> {len(needs_siglip)} need SigLIP | {len(needs_faces)} need faces")

        # Phase 2: SigLIP visual + zero-shot tags (batch 64, fused)
        if needs_siglip:
            t0 = time.time()
            print(f"Phase 2/4: SigLIP visual + tags ({len(needs_siglip)} images)")
            sys.stdout.flush()

            blip_queue = []  # (path, image_id, image, fast_tags, colors)
            batch_sz = 64
            n_batches = (len(needs_siglip) + batch_sz - 1) // batch_sz

            for batch_idx in tqdm(range(n_batches), desc="SigLIP batches", unit="batch"):
                start = batch_idx * batch_sz
                batch_items = needs_siglip[start:start + batch_sz]
                loaded = []  # (path, iid, image)

                for path, iid in batch_items:
                    try:
                        img = self._open_resized(path, max_dim=384)
                        loaded.append((path, iid, img))
                    except Exception as e:
                        print(f"\n  Load fail {path.name}: {e}")
                        sys.stdout.flush()

                if not loaded:
                    continue

                batch_t0 = time.time()
                try:
                    images = [img for _, _, img in loaded]
                    ids = [iid for _, iid, _ in loaded]
                    vecs = self._embed_siglip_batch(images)
                    self.siglip_index.add_with_ids(
                        vecs, np.array(ids, dtype=np.int64)
                    )
                    for iid in ids:
                        self._existing_siglip_ids.add(iid)

                    # Classify tags per image
                    for path, iid, img in loaded:
                        try:
                            tags, conf = self._classify_tags_fast(img, top_k=8, threshold=0.30)
                            colors = self._extract_colors(img)
                            is_screen = self._is_screenshot(path, img)

                            if conf < 0.35 and not is_screen:
                                blip_queue.append((path, iid, img, tags, colors))
                            else:
                                caption = self._tags_to_caption(tags, colors)
                                self.conn.execute(
                                    "UPDATE images SET caption=?, tags=?, colors=?, tag_confidence=? WHERE id=?",
                                    (caption, json.dumps(tags), json.dumps(colors), conf, iid)
                                )
                                img.close()
                        except Exception as e:
                            print(f"\n  Tag fail {path.name}: {e}")
                            sys.stdout.flush()
                            img.close()

                    batch_dt = time.time() - batch_t0
                    per_img = batch_dt / len(loaded)
                    if batch_idx % 5 == 0:
                        print(f"\n  Batch {batch_idx+1}/{n_batches}: {len(loaded)} imgs in {batch_dt:.1f}s ({per_img:.2f}s/img)")
                        sys.stdout.flush()

                except Exception as e:
                    print(f"\n  SigLIP batch fail: {e}")
                    sys.stdout.flush()
                    for _, _, img in loaded:
                        img.close()

            self._save_indices()
            self.conn.commit()
            gc.collect()
            print(f"\nPhase 2 done: {time.time()-t0:.1f}s | {len(blip_queue)} need BLIP")
            sys.stdout.flush()

            # Phase 3: BLIP slow path
            if blip_queue:
                t0 = time.time()
                print(f"Phase 3/4: BLIP slow path ({len(blip_queue)} images)")
                blip_batch_sz = 32

                for i in tqdm(range(0, len(blip_queue), blip_batch_sz), desc="BLIP batches", unit="batch"):
                    batch = blip_queue[i:i + blip_batch_sz]
                    imgs = [item[2] for item in batch]
                    try:
                        captions = self._caption_blip_batch(imgs)
                        for (path, iid, img, fast_tags, colors), cap in zip(batch, captions):
                            merged_tags = fast_tags + [{"tag": cap.lower(), "score": 0.5}]
                            self.conn.execute(
                                "UPDATE images SET caption=?, tags=?, colors=?, tag_confidence=? WHERE id=?",
                                (cap, json.dumps(merged_tags), json.dumps(colors), 0.35, iid)
                            )
                    except Exception as e:
                        print(f"\n  BLIP batch fail: {e}")
                        sys.stdout.flush()
                    finally:
                        for _, _, img, _, _ in batch:
                            img.close()

                self.conn.commit()
                gc.collect()
                print(f"\nPhase 3 done: {time.time()-t0:.1f}s")
            else:
                print("Phase 3/4: BLIP (nothing needed)")
        else:
            print("Phase 2/4: SigLIP (already done)")

        # Phase 4: BGE text embedding
        t0 = time.time()
        print("Phase 4/4: BGE text embedding")
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
            if tag_str: parts.append(f"Tags: {tag_str}.")
            if color_str: parts.append(f"Colors: {color_str}.")
            if cap: parts.append(cap)
            if date: parts.append(f"Date: {date}.")
            if ocr: parts.append(f"Text: {ocr}.")
            synth = " ".join(parts)
            ids_list.append(img_id)
            synth_texts.append(synth)

        if synth_texts:
            chunk = 64
            for i in range(0, len(synth_texts), chunk):
                c_chunk = synth_texts[i:i + chunk]
                id_chunk = np.array(ids_list[i:i + chunk], dtype=np.int64)
                vecs = self._embed_bge_batch(c_chunk)
                self.bge_index.add_with_ids(vecs, id_chunk)
                for iid in ids_list[i:i + chunk]:
                    self._existing_bge_ids.add(iid)
            self._save_indices()
            print(f"Indexed {len(synth_texts)} new in {time.time()-t0:.1f}s")
        else:
            print(f"Nothing new. ({time.time()-t0:.1f}s)")

        # Phase 5: Face detection
        if needs_faces and self.face_app:
            t0 = time.time()
            print("   -> Unloading main-process face model")
            self.face_app = None
            gc.collect()

            person_tags = {"man", "woman", "child", "baby", "group of people", "couple",
                          "family", "selfie", "portrait", "face", "person"}
            face_candidates = []
            for path, iid in needs_faces:
                row = self.conn.execute("SELECT tags FROM images WHERE id=?", (iid,)).fetchone()
                if row and row[0]:
                    tags = json.loads(row[0])
                    tag_names = {t["tag"] for t in tags}
                    if tag_names & person_tags:
                        face_candidates.append((path, iid))

            n_face_workers = 1
            print(f"Phase 5/5: Face detection ({len(face_candidates)}/{len(needs_faces)} images, {n_face_workers} worker)")
            if face_candidates:
                self._run_face_detection_parallel(face_candidates, n_face_workers)
            gc.collect()
            print(f"Time: {time.time()-t0:.1f}s")
        else:
            print("Phase 5/5: Faces (already done)")

        img_count = self.conn.execute("SELECT COUNT(*) FROM images").fetchone()[0]
        person_count = self.conn.execute("SELECT COUNT(*) FROM persons").fetchone()[0]
        print("Done")
        print(f"   Images:  {img_count}")
        print(f"   Persons: {person_count}")
        print(f"   Vectors: {self.siglip_index.ntotal} visual | {self.bge_index.ntotal} text | {self.face_index.ntotal} faces")

    def _run_face_detection_parallel(self, needs_faces: List[Tuple[Path, int]], n_workers: int):
        batch_size = max(1, len(needs_faces) // n_workers)
        worker_batches = []
        for i in range(0, len(needs_faces), batch_size):
            worker_batches.append(needs_faces[i:i + batch_size])

        all_detections = []
        with ProcessPoolExecutor(max_workers=n_workers) as executor:
            futures = {
                executor.submit(_detect_faces_worker, batch, str(self.dirs["models"]), str(self.dirs["face_crops"])): batch
                for batch in worker_batches
            }
            for future in tqdm(as_completed(futures), total=len(futures), desc="Face workers", unit="batch"):
                try:
                    batch_results = future.result()
                    all_detections.extend(batch_results)
                except Exception as e:
                    print(f"Face worker failed: {e}")

        for image_id, embeddings, bboxes, crop_paths in all_detections:
            face_db_ids = []
            for emb, bbox, crop_path in zip(embeddings, bboxes, crop_paths):
                emb_norm = emb / (np.linalg.norm(emb) + 1e-8)
                emb_blob = emb.astype(np.float32).tobytes()

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
                        (emb_blob, crop_path, f"Person {self.face_index.ntotal + 1}")
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
                    (image_id, person_id, json.dumps([int(x) for x in bbox]), emb_blob)
                )

            if face_db_ids:
                self.conn.execute(
                    "UPDATE images SET face_count=?, face_ids=? WHERE id=?",
                    (len(face_db_ids), json.dumps([int(fid) for fid in face_db_ids]), image_id)
                )

        self.conn.commit()
        self._save_indices()

    # --- Query Engine ------------------------------------------------

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
        if self.siglip_model is None and self._siglip_onnx_session is None:
            raise RuntimeError("SigLIP not loaded")
        if self.bge_model is None:
            raise RuntimeError("BGE not loaded")

        expanded_queries, expanded_terms = self._expand_query(text)
        face_pid = self._extract_face_filter(text)

        face_img_set: Optional[Set[int]] = None
        if face_pid:
            rows = self.conn.execute(
                "SELECT DISTINCT image_id FROM face_appearances WHERE person_id=?",
                (face_pid,)
            ).fetchall()
            face_img_set = {r[0] for r in rows}
            print(f"   Filtering by person ID {face_pid} ({len(face_img_set)} photos)")

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
            print("   No vector matches with face filter; broadening...")
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

    # --- Human-in-the-Loop -------------------------------------------

    def name_person(self, person_id: int, name: str):
        self.conn.execute("UPDATE persons SET name=? WHERE id=?", (name, person_id))
        self.conn.commit()
        print(f"Person {person_id} named '{name}'")

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
        print(f"Caption updated & re-indexed for image {image_id}")

    def add_feedback(self, image_id: int, query: str, was_relevant: bool, notes: str = ""):
        self.conn.execute(
            "INSERT INTO feedback (image_id, query_text, was_relevant, notes) VALUES (?, ?, ?, ?)",
            (image_id, query, 1 if was_relevant else 0, notes)
        )
        self.conn.commit()
        print(f"Feedback recorded for image {image_id}")

    # --- Stats -------------------------------------------------------

    def stats(self):
        img_count = self.conn.execute("SELECT COUNT(*) FROM images").fetchone()[0]
        person_count = self.conn.execute("SELECT COUNT(*) FROM persons").fetchone()[0]
        face_total = self.conn.execute("SELECT COALESCE(SUM(face_count), 0) FROM images").fetchone()[0]
        feedback_count = self.conn.execute("SELECT COUNT(*) FROM feedback").fetchone()[0]

        print(f"Fotoro Archive Stats")
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


# --- Parallel Worker Function (module-level for pickle) ------------

def _detect_faces_worker(batch: List[Tuple[Path, int]], models_root: str, face_crops_dir: str):
    from insightface.app import FaceAnalysis
    import numpy as np
    from PIL import Image

    face_crops_path = Path(face_crops_dir)
    face_crops_path.mkdir(exist_ok=True)

    app = FaceAnalysis(
        name="buffalo_l",
        root=models_root,
        providers=["CPUExecutionProvider"]
    )
    app.prepare(ctx_id=0, det_size=(640, 640))

    results = []
    for path, image_id in batch:
        image = None
        try:
            image = Image.open(path).convert("RGB")
            w, h = image.size

            name = path.name.lower()
            is_screen = any(hint in name for hint in ["screenshot", "screencap", "snap", "screen_"]) or (max(w, h) / min(w, h) > 2.2)
            if is_screen:
                continue

            if max(w, h) > 640:
                scale = 640 / max(w, h)
                image = image.resize((int(w * scale), int(h * scale)), Image.Resampling.LANCZOS)

            img_np = np.array(image)
            faces = app.get(img_np)

            embeddings = []
            bboxes = []
            crop_paths = []

            for i, face in enumerate(faces):
                bbox = face.bbox.astype(float)
                face_area = (bbox[2] - bbox[0]) * (bbox[3] - bbox[1])
                img_area = img_np.shape[0] * img_np.shape[1]
                if face_area / img_area < 0.002:
                    continue
                if hasattr(face, 'det_score') and face.det_score < 0.70:
                    continue
                if hasattr(face, 'pose') and abs(face.pose[1]) > 50:
                    continue

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
                crop_path = str(face_crops_path / f"img{image_id}_face{i}.jpg")
                crop.save(crop_path, quality=85)

                embeddings.append(face.embedding.astype(np.float32))
                bboxes.append([int(x1), int(y1), int(x2), int(y2)])
                crop_paths.append(crop_path)

            if embeddings:
                results.append((image_id, embeddings, bboxes, crop_paths))
            else:
                results.append((image_id, [], [], []))

        except Exception:
            results.append((image_id, [], [], []))
        finally:
            if image is not None:
                image.close()

    return results
