#!/usr/bin/env bash
# Fotoro — remove Tailscale so setup can install a clean instance
set -euo pipefail

echo "[TAILSCALE] Stopping and disconnecting…"
if command -v tailscale >/dev/null 2>&1; then
  sudo tailscale logout 2>/dev/null || true
  sudo tailscale down 2>/dev/null || true
fi
sudo systemctl stop tailscaled 2>/dev/null || true
sudo systemctl disable tailscaled 2>/dev/null || true

# Wipe machine identity — otherwise reinstall auto-reconnects without login
if [[ -d /var/lib/tailscale ]]; then
  echo "[TAILSCALE] Clearing saved machine identity…"
  sudo rm -rf /var/lib/tailscale/*
fi

if command -v dnf >/dev/null 2>&1; then
  echo "[TAILSCALE] Removing via dnf…"
  sudo dnf remove -y tailscale 2>/dev/null || true
elif command -v apt-get >/dev/null 2>&1; then
  echo "[TAILSCALE] Removing via apt…"
  sudo apt-get remove -y tailscale 2>/dev/null || true
elif command -v pacman >/dev/null 2>&1; then
  echo "[TAILSCALE] Removing via pacman…"
  sudo pacman -R --noconfirm tailscale 2>/dev/null || true
fi

if command -v tailscale >/dev/null 2>&1; then
  echo "[WARN] tailscale binary still present — remove the package manually if needed."
else
  echo "[TAILSCALE] Uninstall complete."
fi
