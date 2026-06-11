#!/bin/bash
set -e

echo "=== Fotoro Speed Optimizer (3B Model) ==="

# ── 1. CPU Governor ───────────────────────────────────────────────────────────
echo "[KERNEL] Setting CPU governor to performance..."
cpupower frequency-set -g performance 2>/dev/null ||   for f in /sys/devices/system/cpu/cpu*/cpufreq/scaling_governor; do
    echo performance > "$f" 2>/dev/null || true
  done

# ── 2. Huge Pages ─────────────────────────────────────────────────────────────
echo "[KERNEL] Allocating huge pages..."
echo 1024 | tee /proc/sys/vm/nr_hugepages >/dev/null 2>&1 || true

# ── 3. Detect Physical Cores ──────────────────────────────────────────────────
PHY_CORES=$(lscpu -p | grep -v '^#' | cut -d, -f2 | sort -u | wc -l)
echo "[KERNEL] Detected $PHY_CORES physical cores"

# ── 4. OpenBLAS / OpenMP Tuning ───────────────────────────────────────────────
export OPENBLAS_NUM_THREADS=$PHY_CORES
export GOMP_CPU_AFFINITY="0-$((PHY_CORES-1))"
export OMP_NUM_THREADS=$PHY_CORES

# ── 5. Find llama-server binary ───────────────────────────────────────────────
LLAMA_SERVER=""
for path in   "./models/llama.cpp/build/bin/llama-server"   "./llama.cpp/build/bin/llama-server"   "./llama-server"   "/usr/local/bin/llama-server"; do
  if [ -x "$path" ]; then
    LLAMA_SERVER="$path"
    break
  fi
done

if [ -z "$LLAMA_SERVER" ]; then
  echo "[ERROR] llama-server not found. Build it:"
  echo "  cd models/llama.cpp && cmake -B build -DLLAMA_AVX2=ON -DCMAKE_BUILD_TYPE=Release"
  echo "  cmake --build build --config Release -j$PHY_CORES"
  exit 1
fi

echo "[LLM] Found llama-server at: $LLAMA_SERVER"

# ── 6. 3B Model Paths ─────────────────────────────────────────────────────────
export LLAMA_SERVER_BIN="$LLAMA_SERVER"
export FOTORO_MODEL_PATH="${FOTORO_MODEL_PATH:-./models/Qwen2.5-VL-3B-Instruct-Q4_K_M.gguf}"
export FOTORO_MMPROJ_PATH="${FOTORO_MMPROJ_PATH:-./models/mmproj-Qwen2.5-VL-3B-Instruct-Q8_0.gguf}"
export LLAMA_THREADS="${LLAMA_THREADS:-$PHY_CORES}"
export LLAMA_CTX_SIZE="${LLAMA_CTX_SIZE:-2048}"
export LLAMA_FLASH_ATTN="${LLAMA_FLASH_ATTN:-1}"

# ── 7. Fotoro Environment ─────────────────────────────────────────────────────
export FOTORO_DB="${FOTORO_DB:-./fotoro.db}"
export FOTORO_MODEL="${FOTORO_MODEL:-qwen2.5-vl-3b}"
export FOTORO_ADDR="${FOTORO_ADDR:-127.0.0.1:8765}"
export EMBED_ADDR="${EMBED_ADDR:-http://127.0.0.1:8082}"

# ── 8. Start embed server (if not running) ───────────────────────────────────
if ! curl -s "$EMBED_ADDR/health" >/dev/null 2>&1; then
  echo "[EMBED] Starting embed server on $EMBED_ADDR..."
  nohup "$LLAMA_SERVER"     -m ./models/nomic-embed-text-v1.5.f16.gguf     --host 127.0.0.1 --port 8082     --embeddings --mlock --no-mmap     >/tmp/fotoro-embed.log 2>&1 &
  sleep 3
fi

echo ""
echo "=== Environment Ready (3B) ==="
echo "Physical cores: $PHY_CORES"
echo "llama-server:   $LLAMA_SERVER"
echo "VLM model:      $FOTORO_MODEL_PATH"
echo "Embed model:    ./models/nomic-embed-text-v1.5.f16.gguf"
echo "Embed server:   $EMBED_ADDR"
echo ""

# ── 9. Build fotoro if not present ────────────────────────────────────────────
if [ ! -f "./fotoro" ]; then
  echo "[BUILD] Building fotoro binary..."
  go build -ldflags="-s -w" -o fotoro .
  echo "[BUILD] Done."
fi

# ── 10. Run with taskset pinning ──────────────────────────────────────────────
echo "[RUN] Starting: $@"
echo ""

if command -v taskset >/dev/null 2>&1; then
  exec taskset -c "0-$((PHY_CORES-1))" "$@"
else
  exec "$@"
fi