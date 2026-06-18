package cmd

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// LoadDotEnv loads KEY=VALUE pairs from .env files.
// Searches the current directory, the fotoro binary directory, and FOTORO_DB's directory.
// Existing non-empty environment variables are not overwritten.
func LoadDotEnv() {
	seen := map[string]bool{}
	for _, path := range dotEnvPaths() {
		if seen[path] {
			continue
		}
		seen[path] = true
		loadDotEnvFile(path)
	}
}

func dotEnvPaths() []string {
	var paths []string
	add := func(dir string) {
		if dir == "" {
			return
		}
		paths = append(paths, filepath.Join(dir, ".env"))
	}

	if cwd, err := os.Getwd(); err == nil {
		add(cwd)
	}
	if exe, err := os.Executable(); err == nil {
		add(filepath.Dir(exe))
	}
	if dbPath := os.Getenv("FOTORO_DB"); dbPath != "" {
		add(filepath.Dir(dbPath))
	} else {
		add(".")
	}

	return paths
}

func loadDotEnvFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if after, ok := strings.CutPrefix(line, "export "); ok {
			line = strings.TrimSpace(after)
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		val = strings.Trim(val, `"'`)
		if key == "" {
			continue
		}
		if os.Getenv(key) == "" && val != "" {
			os.Setenv(key, val)
		}
	}
}
