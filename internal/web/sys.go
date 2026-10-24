package web

import (
	"fmt"
	"os"
)

func env(k string) string { return os.Getenv(k) }

func stdout() *os.File { return os.Stdout }

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// OpenBrowser opens the panel in the default browser.
func OpenBrowser(url string) {
	_ = openBrowser(url)
	fmt.Fprintln(os.Stderr, url)
}
