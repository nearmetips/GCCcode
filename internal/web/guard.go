package web

import (
	"net"
	"net/http"
	"strings"
)

// actionHeader must be on every write. A page on another site can make the
// browser send requests to 127.0.0.1, but it cannot add this header without a
// CORS preflight, and the panel never grants one.
const actionHeader = "X-GCCCode"

// guard keeps other websites out of the panel. The Host check stops DNS
// rebinding, where a hostile domain resolves to 127.0.0.1 and reads the API.
func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHost(r.Host) {
			http.Error(w, "面板只接受本机地址访问", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			origin := r.Header.Get("Origin")
			if r.Header.Get(actionHeader) != "1" || origin != "" && origin != "http://"+r.Host {
				http.Error(w, "请在 GCC Code 面板里操作", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
