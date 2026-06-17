#!/bin/bash
set -e

RED='\\033[0;31m'
GREEN='\\033[0;32m'
BLUE='\\033[0;34m'
YELLOW='\\033[1;33m'
NC='\\033[0m'

echo -e "${BLUE}"
echo "╔════════════════════════════════════════════════════════════════╗"
echo "║              Fotoro Photo Server Installer                     ║"
echo "║         Secure self-hosted photo intelligence                ║"
echo "║              No daemons. No auto-start. You control.          ║"
echo "╚════════════════════════════════════════════════════════════════╝"
echo -e "${NC}"

# ── 1. Check dependencies ───────────────────────────────────────────────────
echo -e "${BLUE}[CHECK]${NC} Checking dependencies..."

MISSING_DEPS=()

if ! command -v go &> /dev/null; then
    MISSING_DEPS+=("Go (https://go.dev/dl)")
fi

if ! command -v cmake &> /dev/null; then
    MISSING_DEPS+=("CMake (sudo dnf install cmake)")
fi

if ! command -v git &> /dev/null; then
    MISSING_DEPS+=("Git (sudo dnf install git)")
fi

if ! command -v gcc &> /dev/null; then
    MISSING_DEPS+=("GCC (sudo dnf install gcc)")
fi

if [ ${#MISSING_DEPS[@]} -gt 0 ]; then
    echo -e "${RED}[ERROR]${NC} Missing dependencies:"
    for dep in "${MISSING_DEPS[@]}"; do
        echo "  • $dep"
    done
    exit 1
fi

echo -e "${GREEN}[OK]${NC} All dependencies found"

# ── 2. Install Tailscale (optional, user can do later) ──────────────────────
if ! command -v tailscale &> /dev/null; then
    echo -e "${YELLOW}[INFO]${NC} Tailscale not installed. You'll need it for remote access."
    echo "Install later with: curl -fsSL https://tailscale.com/install.sh | sh"
else
    echo -e "${GREEN}[OK]${NC} Tailscale already installed"
fi

# ── 3. Install fonts ────────────────────────────────────────────────────────
echo -e "${BLUE}[INSTALL]${NC} Installing fonts..."
FONT_DIR="$HOME/.local/share/fonts"
mkdir -p "$FONT_DIR"

if [ ! -f "$FONT_DIR/Inter-Regular.ttf" ]; then
    echo "  Downloading Inter font..."
    wget -q "https://github.com/rsms/inter/releases/download/v4.0/Inter-4.0.zip" -O /tmp/inter.zip 2>/dev/null || true
    if [ -f /tmp/inter.zip ]; then
        unzip -q /tmp/inter.zip -d /tmp/inter 2>/dev/null || true
        cp /tmp/inter/*.ttf "$FONT_DIR/" 2>/dev/null || true
        fc-cache -f "$FONT_DIR" 2>/dev/null || true
    fi
fi

if [ ! -f "$FONT_DIR/JetBrainsMono-Regular.ttf" ]; then
    echo "  Downloading JetBrains Mono..."
    wget -q "https://github.com/JetBrains/JetBrainsMono/releases/download/v2.304/JetBrainsMono-2.304.zip" -O /tmp/jbmono.zip 2>/dev/null || true
    if [ -f /tmp/jbmono.zip ]; then
        unzip -q /tmp/jbmono.zip -d /tmp/jbmono 2>/dev/null || true
        cp /tmp/jbmono/fonts/ttf/*.ttf "$FONT_DIR/" 2>/dev/null || true
        fc-cache -f "$FONT_DIR" 2>/dev/null || true
    fi
fi

echo -e "${GREEN}[OK]${NC} Fonts installed"

# ── 4. Build llama.cpp ──────────────────────────────────────────────────────
echo -e "${BLUE}[BUILD]${NC} Building llama.cpp..."
if [ ! -d "models/llama.cpp" ]; then
    mkdir -p models
    git clone --depth 1 https://github.com/ggerganov/llama.cpp.git models/llama.cpp
fi

cd models/llama.cpp
git pull origin master 2>/dev/null || true

cmake -B build \\
    -DLLAMA_AVX2=ON \\
    -DLLAMA_AVX512=OFF \\
    -DLLAMA_NATIVE=ON \\
    -DCMAKE_BUILD_TYPE=Release \\
    -DLLAMA_BUILD_TESTS=OFF \\
    -DLLAMA_BUILD_EXAMPLES=OFF \\
    -DLLAMA_BUILD_SERVER=ON

cmake --build build --config Release -j$(nproc)
cd ../..

echo -e "${GREEN}[OK]${NC} llama.cpp built"

# ── 5. Download models ──────────────────────────────────────────────────────
echo -e "${BLUE}[DOWNLOAD]${NC} Downloading models..."
mkdir -p models

MODEL_URL="https://huggingface.co/Qwen/Qwen2.5-VL-3B-Instruct-GGUF/resolve/main/Qwen2.5-VL-3B-Instruct-Q4_K_M.gguf"
MMPROJ_URL="https://huggingface.co/Qwen/Qwen2.5-VL-3B-Instruct-GGUF/resolve/main/mmproj-Qwen2.5-VL-3B-Instruct-Q8_0.gguf"
EMBED_URL="https://huggingface.co/nomic-ai/nomic-embed-text-v1.5-GGUF/resolve/main/nomic-embed-text-v1.5.f16.gguf"

if [ ! -f "models/Qwen2.5-VL-3B-Instruct-Q4_K_M.gguf" ]; then
    echo "  Downloading Qwen2.5-VL-3B..."
    wget -q --show-progress "$MODEL_URL" -O models/Qwen2.5-VL-3B-Instruct-Q4_K_M.gguf || \\
    curl -L -o models/Qwen2.5-VL-3B-Instruct-Q4_K_M.gguf "$MODEL_URL"
fi

if [ ! -f "models/mmproj-Qwen2.5-VL-3B-Instruct-Q8_0.gguf" ]; then
    echo "  Downloading mmproj..."
    wget -q --show-progress "$MMPROJ_URL" -O models/mmproj-Qwen2.5-VL-3B-Instruct-Q8_0.gguf || \\
    curl -L -o models/mmproj-Qwen2.5-VL-3B-Instruct-Q8_0.gguf "$MMPROJ_URL"
fi

if [ ! -f "models/nomic-embed-text-v1.5.f16.gguf" ]; then
    echo "  Downloading nomic-embed..."
    wget -q --show-progress "$EMBED_URL" -O models/nomic-embed-text-v1.5.f16.gguf || \\
    curl -L -o models/nomic-embed-text-v1.5.f16.gguf "$EMBED_URL"
fi

echo -e "${GREEN}[OK]${NC} Models downloaded"

# ── 6. Build fotoro ─────────────────────────────────────────────────────────
echo -e "${BLUE}[BUILD]${NC} Building fotoro..."
go mod tidy
go build -ldflags="-s -w" -o fotoro .

echo -e "${GREEN}[OK]${NC} fotoro built"

# ── 7. Create .env if not exists ────────────────────────────────────────────
if [ ! -f ".env" ]; then
    echo -e "${BLUE}[CONFIG]${NC} Creating .env file..."
    cat > .env <<'EOF'
# Supabase (required for auth and online features)
SUPABASE_URL=""
SUPABASE_ANON_KEY=""
SUPABASE_SERVICE_KEY=""

# Google OAuth (get from https://console.cloud.google.com/apis/credentials)
GOOGLE_CLIENT_ID=""
GOOGLE_CLIENT_SECRET=""

# Tailscale (optional - can configure via GUI)
TAILSCALE_AUTH_KEY=""

# Fotoro
FOTORO_DB="./fotoro.db"
FOTORO_ADDR="127.0.0.1:8765"
FOTORO_MODEL="qwen2.5-vl-3b"
FOTORO_NODE_NAME="fotoro-server"

# LLM
LLAMA_ADDR="http://127.0.0.1:8081"
EMBED_ADDR="http://127.0.0.1:8082"
FOTORO_MODEL_PATH="./models/Qwen2.5-VL-3B-Instruct-Q4_K_M.gguf"
FOTORO_MMPROJ_PATH="./models/mmproj-Qwen2.5-VL-3B-Instruct-Q8_0.gguf"
EOF
    echo -e "${YELLOW}[IMPORTANT]${NC} Edit .env with your Supabase and Google OAuth credentials"
fi

# ── 8. Final instructions ───────────────────────────────────────────────────
echo ""
echo -e "${GREEN}════════════════════════════════════════════════════════════════${NC}"
echo -e "${GREEN}  ✅ Fotoro Installation Complete!${NC}"
echo -e "${GREEN}════════════════════════════════════════════════════════════════${NC}"
echo ""
echo "⚠️  NO DAEMONS INSTALLED - You have full control"
echo ""
echo "Next steps:"
echo "  1. Edit .env with your credentials"
echo "  2. Run: ./fotoro setup        # First-time configuration"
echo "  3. Run: ./fotoro app          # Start the GUI"
echo ""
echo "Commands:"
echo "  ./fotoro server                 # Start API server"
echo "  ./fotoro tailscale connect      # Connect VPN"
echo "  ./fotoro tailscale disconnect   # Disconnect VPN"
echo "  ./fotoro scheduler              # Process pending uploads"
echo ""
echo "To stop: Press Ctrl+C or close the window"
echo "No background processes will remain running"
echo ""
