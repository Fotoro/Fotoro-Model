#!/usr/bin/env bash
# Fotoro — Tailscale installer (called automatically after Fotoro sign-in)
set -euo pipefail

if command -v tailscale >/dev/null 2>&1; then
  echo "[TAILSCALE] Already installed: $(tailscale version 2>/dev/null | head -1 || echo tailscale)"
  exit 0
fi

echo "[TAILSCALE] Installing Tailscale (official installer)..."
if ! command -v curl >/dev/null 2>&1; then
  echo "[ERROR] curl is required. Install curl and retry."
  exit 1
fi

curl -fsSL https://tailscale.com/install.sh | sh

if ! command -v tailscale >/dev/null 2>&1; then
  echo "[ERROR] Tailscale install finished but 'tailscale' was not found in PATH."
  exit 1
fi

echo "[TAILSCALE] Install complete."
