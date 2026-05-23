#!/usr/bin/env python3
"""
Fotoro Terminal Interface v4.1 — Crash‑resistant, results first
"""
import argparse
import sys
from pathlib import Path
from pipeline import FotoroPipeline


def main():
    parser = argparse.ArgumentParser(prog="fotoro", description="Fotoro — Local Semantic Photo Archive")
    subs = parser.add_subparsers(dest="command")

    # ingest
    ing = subs.add_parser("ingest", help="Ingest a folder of images")
    ing.add_argument("folder", help="Path to image folder")
    ing.add_argument("-r", "--recursive", action="store_true", help="Scan subfolders")
    ing.add_argument("--no-faces", action="store_true", help="Skip face detection")

    # query
    qry = subs.add_parser("query", help="Semantic search")
    qry.add_argument("text", help="Natural language query")
    qry.add_argument("-k", "--top-k", type=int, default=10, help="Results to show")
    qry.add_argument("--show-ocr", action="store_true", help="Show OCR text if present")

    # stats
    subs.add_parser("stats", help="Show archive statistics")

    # persons
    psub = subs.add_parser("persons", help="List detected face clusters")
    psub.add_argument("--photos", type=int, metavar="ID", help="Show photo IDs for a person")

    # name-face
    nf = subs.add_parser("name-face", help="Name a face cluster")
    nf.add_argument("id", type=int, help="Person ID")
    nf.add_argument("name", help="Name to assign")

    # correct-caption
    cc = subs.add_parser("correct-caption", help="Fix a caption and re-index")
    cc.add_argument("id", type=int, help="Image ID")
    cc.add_argument("caption", help="New caption text")

    # feedback
    fb = subs.add_parser("feedback", help="Log human feedback for a result")
    fb.add_argument("id", type=int, help="Image ID")
    fb.add_argument("query", help="Query that produced this result")
    fb.add_argument("--bad", action="store_true", help="Mark as irrelevant")
    fb.add_argument("--note", default="", help="Optional note")

    # merge-albums
    ma = subs.add_parser("merge-albums", help="Merge duplicate face albums")
    ma.add_argument("--threshold", type=float, default=0.72, help="Similarity threshold")

    # cleanup-albums
    ca = subs.add_parser("cleanup-albums", help="Remove low-quality single-photo albums")
    ca.add_argument("--min-photos", type=int, default=2, help="Minimum photos to keep")

    # rename-album
    ra = subs.add_parser("rename-album", help="Rename a person album")
    ra.add_argument("id", type=int, help="Person ID")
    ra.add_argument("name", help="New name")

    args = parser.parse_args()
    if not args.command:
        parser.print_help()
        sys.exit(1)

    pipe = FotoroPipeline()

    if args.command == "ingest":
        pipe.load_models(load_face=not args.no_faces)
        pipe.ingest_folder(args.folder, recursive=args.recursive)

    elif args.command == "query":
        pipe.load_models(load_face=False)
        results = pipe.query(args.text, top_k=args.top_k)

        if not results:
            print("\n No results found.\n")
            return

        print(f"\n Query: '{args.text}' — Top {len(results)} results\n")
        for i, r in enumerate(results, 1):
            print(f"{i}. [score {r['score']}] {r['filename']}  (ID:{r['id']})")
            print(f"    {r['path']}")
            if r['caption']:
                cap = r['caption'][:160] + "…" if len(r['caption']) > 160 else r['caption']
                print(f"    {cap}")
            # FIX: tags are stored as dicts like {"tag": "beach", "score": 0.5}
            if r['tags']:
                tag_names = [t.get("tag", str(t)) for t in r['tags'][:6]]
                print(f"     {', '.join(tag_names)}")
            if r['colors']:
                print(f"     {', '.join(r['colors'])}")
            if args.show_ocr and r['ocr']:
                print(f"    OCR: {r['ocr'][:120]}")
            meta = []
            if r['date']:
                meta.append(f" {r['date']}")
            if r['face_count']:
                meta.append(f" {r['face_count']} faces")
            meta.append(f" {r['resolution']}")
            print(f"   {' | '.join(meta)}")
            print()

    elif args.command == "stats":
        # DB & FAISS indices are already loaded in __init__; no models needed
        pipe.stats()

    elif args.command == "persons":
        if args.photos:
            photo_ids = pipe.get_person_photos(args.photos)
            print(f"\n Person {args.photos} appears in {len(photo_ids)} photos:")
            print(f"   IDs: {photo_ids[:50]}{'...' if len(photo_ids) > 50 else ''}")
        else:
            rows = pipe.list_persons()
            print(f"\n Detected Persons ({len(rows)})\n")
            for pid, name, count, sample in rows:
                print(f"  ID {pid}: {name} — {count} photos")
                if sample:
                    print(f"         Sample crop: {sample}")

    elif args.command == "name-face":
        pipe.name_person(args.id, args.name)

    elif args.command == "correct-caption":
        pipe.load_models(load_face=False)
        pipe.correct_caption(args.id, args.caption)

    elif args.command == "feedback":
        pipe.add_feedback(args.id, args.query, not args.bad, args.note)

    elif args.command == "merge-albums":
        print("merge-albums: not yet implemented in this pipeline version.")
        # Uncomment once pipeline.py has merge_similar_persons():
        # pipe.load_models(load_face=True)
        # pipe.merge_similar_persons(threshold=args.threshold)

    elif args.command == "cleanup-albums":
        print("cleanup-albums: not yet implemented in this pipeline version.")
        # Uncomment once pipeline.py has cleanup_bad_crops():
        # pipe.load_models(load_face=True)
        # pipe.cleanup_bad_crops(min_photos=args.min_photos)

    elif args.command == "rename-album":
        pipe.name_person(args.id, args.name)


if __name__ == "__main__":
    main()
