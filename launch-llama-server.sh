#!/bin/bash
# ═══════════════════════════════════════════════════════════════════════════════
# AGGRESSIVE SPEED — Qwen2.5-VL-3B-Instruct (CPU, sub-25s target)
# ═══════════════════════════════════════════════════════════════════════════════

LLAMA_SERVER="./models/llama.cpp/build/bin/llama-server"

# AGGRESSIVE CPU OPTIMIZATIONS:
# --ctx-size 512: Bare minimum for single image + short caption
# --batch-size 256 / --ubatch-size 256: Tight batches for CPU cache
# --image-min-tokens 32 / --image-max-tokens 128: Minimal vision tokens
# --no-mmap: Direct memory access
# --flash-attn: Saves ~20% compute on vision layers

$LLAMA_SERVER \
  -m ./models/Qwen2.5-VL-3B-Instruct-Q4_K_M.gguf \
  --mmproj ./models/mmproj-Qwen2.5-VL-3B-Instruct-Q8_0.gguf \
  --host 127.0.0.1 \
  --port 8081 \
  --threads 4 \
  --ctx-size 512 \
  --batch-size 256 \
  --ubatch-size 256 \
  --cache-type-k q8_0 \
  --cache-type-v q8_0 \
  --no-mmap \
  --image-min-tokens 32 \
  --image-max-tokens 128 \
  --flash-attn