package claude

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/nearmetips/DailiCode/internal/core"
)

// device is the pair Claude Code stores in .claude.json and sends with the
// subscription. Two accounts that share either value are one device to the
// vendor.
type device struct {
	MachineID string `json:"machineID"`
	UserID    string `json:"userID"`
}

func readDevice(path string) device {
	b, err := os.ReadFile(path)
	if err != nil {
		return device{}
	}
	var d device
	_ = json.Unmarshal(b, &d)
	return d
}

func clearDevice(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	if _, ok := m["machineID"]; !ok {
		if _, ok := m["userID"]; !ok {
			return nil
		}
	}
	delete(m, "machineID")
	delete(m, "userID")
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return core.WriteFileAtomic(path, append(out, '\n'), 0o600)
}

// SeparateDevices clears a copied machineID or userID so Claude Code mints a
// new pair on next launch. The account that already owned the id (the main
// home, otherwise the oldest) keeps it.
func SeparateDevices(s core.Settings, accounts []core.Account) ([]string, error) {
	type item struct {
		acc    core.Account
		device device
		path   string
	}
	var items []item
	for _, a := range accounts {
		if a.Kind != core.KindClaude || a.ConfigDir == "" {
			continue
		}
		path := ConfigJSON(s, a.ConfigDir)
		d := readDevice(path)
		if d.MachineID == "" && d.UserID == "" {
			continue
		}
		items = append(items, item{acc: a, device: d, path: path})
	}
	keep := map[string]string{}
	for _, it := range items {
		for _, id := range []string{it.device.MachineID, it.device.UserID} {
			if id == "" {
				continue
			}
			prev, ok := keep[id]
			if !ok || preferKeep(s, it.acc, accountByID(accounts, prev)) {
				keep[id] = it.acc.ID
			}
		}
	}
	var cleared []string
	seen := map[string]bool{}
	for _, it := range items {
		drop := false
		if id := it.device.MachineID; id != "" && keep[id] != it.acc.ID {
			drop = true
		}
		if id := it.device.UserID; id != "" && keep[id] != it.acc.ID {
			drop = true
		}
		if !drop || seen[it.acc.ID] {
			continue
		}
		if err := clearDevice(it.path); err != nil {
			return cleared, err
		}
		seen[it.acc.ID] = true
		cleared = append(cleared, it.acc.ID)
	}
	return cleared, nil
}

func preferKeep(s core.Settings, a, b core.Account) bool {
	aMain := IsMainHome(s, a.ConfigDir)
	bMain := IsMainHome(s, b.ConfigDir)
	if aMain != bMain {
		return aMain
	}
	return a.CreatedAt.Before(b.CreatedAt)
}

func accountByID(accounts []core.Account, id string) core.Account {
	for _, a := range accounts {
		if a.ID == id {
			return a
		}
	}
	return core.Account{}
}

// DeviceClashes reports accounts that still share a machineID or userID.
func DeviceClashes(s core.Settings, accounts []core.Account) []string {
	owner := map[string]string{}
	var lines []string
	for _, a := range accounts {
		if a.Kind != core.KindClaude || a.ConfigDir == "" {
			continue
		}
		d := readDevice(ConfigJSON(s, a.ConfigDir))
		for _, id := range []string{d.MachineID, d.UserID} {
			if id == "" {
				continue
			}
			if prev, ok := owner[id]; ok && prev != a.ID {
				lines = append(lines, fmt.Sprintf("%s 和 %s 的设备标识相同", a.ID, prev))
			} else {
				owner[id] = a.ID
			}
		}
	}
	return lines
}

// ProxyProblem is why Claude Code cannot use proxy as an egress, or "" when
// it can. Claude Code honours only http and https proxies; it ignores a SOCKS
// one and quietly leaves from the machine's own address instead.
func ProxyProblem(proxy string) string {
	p := strings.TrimSpace(proxy)
	if p == "" {
		return ""
	}
	u, err := url.Parse(p)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "代理地址要带协议，例如 http://127.0.0.1:7890"
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return ""
	case "socks", "socks4", "socks4a", "socks5", "socks5h":
		return "Claude Code 不支持 SOCKS 代理，会悄悄改走本机出口，和别的号撞在同一个 IP 上。请改用 http:// 代理（Clash 的混合端口同时支持 http）"
	default:
		return "Claude Code 只支持 http:// 或 https:// 代理，不支持 " + u.Scheme + "://"
	}
}

// proxyKey normalises a proxy URL so two spellings of one egress compare
// equal: scheme and host are lowercased, localhost is the loopback address,
// and a trailing slash or path is ignored. The username stays, because
// residential proxies pick a different exit per username on one host:port.
func proxyKey(proxy string) string {
	u, err := url.Parse(strings.TrimSpace(proxy))
	if err != nil || u.Host == "" {
		return strings.TrimSpace(proxy)
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" {
		host = "127.0.0.1"
	}
	key := strings.ToLower(u.Scheme) + "://"
	if u.User != nil {
		key += u.User.Username() + "@"
	}
	return key + host + ":" + u.Port()
}

// SameEgress reports whether two proxy URLs leave through one exit.
func SameEgress(a, b string) bool { return proxyKey(a) == proxyKey(b) }

// EgressProblem is why this Claude account must not start. Two subscriptions
// on one egress are how the vendor groups them. Proxies are compared as
// Claude Code will really use them, see EffectiveProxy.
func EgressProblem(s core.Settings, accounts []core.Account, id string) string {
	var self core.Account
	var others []core.Account
	for _, a := range accounts {
		if a.Kind != core.KindClaude || a.Disabled {
			continue
		}
		if a.ID == id {
			self = a
			continue
		}
		others = append(others, a)
	}
	if self.ID == "" {
		return ""
	}
	proxy := EffectiveProxy(s, self)
	if own := strings.TrimSpace(self.Proxy); own != "" && proxyKey(own) != proxyKey(proxy) {
		return "~/.claude/settings.json 的 env 里写了代理 " + proxy + "，Claude Code 会用它，而不是给这个号设的 " + own + "。把 settings.json 里的代理删掉，或改成同一个"
	}
	if msg := ProxyProblem(proxy); msg != "" {
		return msg
	}
	if len(others) == 0 {
		return ""
	}
	if proxy == "" {
		return "多个 Claude 会员不能走同一出口，否则会被认成一组。给这个号单独设代理：gcccode account proxy " + id + " <url>"
	}
	for _, a := range others {
		if proxyKey(EffectiveProxy(s, a)) == proxyKey(proxy) {
			name := a.Label
			if name == "" {
				name = a.ID
			}
			return "出口代理和 " + name + " 相同。每个 Claude 会员要用自己的代理。"
		}
	}
	return ""
}
