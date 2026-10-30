package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
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

// Running reports whether a GCC Code panel already answers at addr.
func Running(addr string) bool {
	c := http.Client{Timeout: 2 * time.Second}
	r, err := c.Get("http://" + addr + "/api/state")
	if err != nil {
		return false
	}
	defer r.Body.Close()
	var v struct {
		Version string `json:"version"`
	}
	return r.StatusCode == http.StatusOK && json.NewDecoder(r.Body).Decode(&v) == nil && v.Version != ""
}
