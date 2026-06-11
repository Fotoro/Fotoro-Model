#!/bin/bash
# ═══════════════════════════════════════════════════════════════════════════════
# MANUAL LLAMA-SERVER LAUNCH — Qwen2.5-VL-3B-Instruct
# ═══════════════════════════════════════════════════════════════════════════════

LLAMA_SERVER="./models/llama.cpp/build/bin/llama-server"

$LLAMA_SERVER \\
  -m ./models/Qwen2.5-VL-3B-Instruct-Q4_K_M.gguf \\
  --mmproj ./models/mmproj-Qwen2.5-VL-3B-Instruct-Q8_0.gguf \\
  --host 127.0.0.1 \\
  --port 8081 \\
  --threads 4 \\
  --ctx-size 2048 \\
  --batch-size 1024 \\
  --ubatch-size 1024 \\
  --cache-type-k q8_0 \\
  --cache-type-v q8_0 \\
  --mlock \\
  --no-mmap \\
  --cache-prompt \\
  --image-min-tokens 128 \\
  --image-max-tokens 512 \\
  --flash-attn