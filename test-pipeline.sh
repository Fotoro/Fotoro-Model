#!/bin/bash
set -e

echo "═══════════════════════════════════════════════════════════════"
echo "  Fotoro Pipeline Test"
echo "═══════════════════════════════════════════════════════════════"

API="http://127.0.0.1:8765"
TEST_DIR="./test_images"
DB="./test_fotoro.db"

# ── 1. System Check ─────────────────────────────────────────────
echo ""
echo "[TEST 1/6] System specs..."
go run ./cmd/system_check.go 2>/dev/null || echo "  (run manually: go run cmd/system_check.go)"

# ── 2. Health Check ─────────────────────────────────────────────
echo ""
echo "[TEST 2/6] API health..."
if curl -s "$API/api/health" | grep -q "ok"; then
    echo "  ✓ API is healthy"
else
    echo "  ✗ API not responding. Start server first:"
    echo "    ./fotoro server"
    exit 1
fi

# ── 3. Stats ────────────────────────────────────────────────────
echo ""
echo "[TEST 3/6] Database stats..."
curl -s "$API/api/stats" | python3 -m json.tool 2>/dev/null || curl -s "$API/api/stats"

# ── 4. Search (text) ────────────────────────────────────────────
echo ""
echo "[TEST 4/6] Text search..."
curl -s "$API/api/search?q=beach" | python3 -m json.tool 2>/dev/null | head -20 || true

# ── 5. List Images ────────────────────────────────────────────────
echo ""
echo "[TEST 5/6] List images..."
curl -s "$API/api/images?page=1" | python3 -m json.tool 2>/dev/null | head -30 || true

# ── 6. Thumbnail Check ──────────────────────────────────────────
echo ""
echo "[TEST 6/6] Thumbnail check..."
HASH=$(curl -s "$API/api/images?page=1" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d[0]['hash'] if d else '')" 2>/dev/null)
if [ -n "$HASH" ]; then
    STATUS=$(curl -s -o /dev/null -w "%{http_code}" "$API/api/thumbnail/$HASH?size=small")
    if [ "$STATUS" = "200" ]; then
        echo "  ✓ Thumbnail exists for $HASH"
    else
        echo "  ✗ Thumbnail missing for $HASH (status: $STATUS)"
        echo "    Run: ./fotoro backfill-thumbs"
    fi
else
    echo "  ⚠ No images in database to test"
fi

echo ""
echo "═══════════════════════════════════════════════════════════════"
echo "  Pipeline test complete"
echo "═══════════════════════════════════════════════════════════════"
