package app

import (
	"os"
	"path/filepath"
	"strings"
)

// Bundled reports whether this process was started as a macOS application,
// which is how Finder and the Dock run it: argv[0] is then the executable
// inside the bundle. The same binary reached through the ~/.local/bin/gcccode
// symlink has a plain argv[0] and behaves as the command line tool.
func Bundled() bool {
	if len(os.Args) == 0 {
		return false
	}
	return strings.Contains(filepath.ToSlash(os.Args[0]), ".app/Contents/MacOS/")
}

// WidenPath puts the directories where Claude Code, Codex and the rest are
// usually installed on PATH. An app started from Finder inherits only the
// system's PATH, which has none of them.
func WidenPath() {
	home, _ := os.UserHomeDir()
	extra := []string{
		filepath.Join(home, ".local", "bin"),
		"/opt/homebrew/bin", "/usr/local/bin",
		filepath.Join(home, ".npm-global", "bin"), filepath.Join(home, ".bun", "bin"),
		filepath.Join(home, ".volta", "bin"), filepath.Join(home, ".cargo", "bin"),
	}
	if nvm, err := filepath.Glob(filepath.Join(home, ".nvm", "versions", "node", "*", "bin")); err == nil {
		extra = append(extra, nvm...)
	}
	have := map[string]bool{}
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		have[p] = true
	}
	var add []string
	for _, p := range extra {
		if !have[p] {
			if fi, err := os.Stat(p); err == nil && fi.IsDir() {
				add = append(add, p)
			}
		}
	}
	if len(add) > 0 {
		_ = os.Setenv("PATH", strings.Join(append([]string{os.Getenv("PATH")}, add...), string(os.PathListSeparator)))
	}
}
