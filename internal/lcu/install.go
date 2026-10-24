package lcu

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Repo is where LCU releases are published.
const Repo = "amontlabs/lcu"

var githubURL = "https://github.com"

var versionRE = regexp.MustCompile(`^[0-9][0-9A-Za-z.+-]*$`)

// Target is the archive suffix LCU publishes for this machine.
func Target() (string, error) {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "darwin/arm64":
		return "darwin-arm64", nil
	case "linux/arm64":
		return "linux-arm64", nil
	case "linux/amd64":
		return "linux-x64", nil
	}
	return "", fmt.Errorf("LCU 只支持 Apple Silicon 的 macOS 和 ARM64 / x86-64 的 Linux，这台是 %s/%s", runtime.GOOS, runtime.GOARCH)
}

// DefaultApp is where LCU looks for the official ChatGPT desktop app.
func DefaultApp() string {
	if runtime.GOOS == "darwin" {
		return "/Applications/ChatGPT.app"
	}
	return "/usr/lib/chatgpt"
}

// Python finds a Python 3.12+ on PATH, the same search LCU's installer does.
func Python() (string, bool) {
	for _, name := range []string{"python3.14", "python3.13", "python3.12", "python3"} {
		p, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		if exec.Command(p, "-c", "import sys; sys.exit(sys.version_info < (3, 12))").Run() == nil {
			return p, true
		}
	}
	return "", false
}

// Download selects one release archive.
type Download struct {
	Version string // empty means the latest release
	Proxy   string
	Mirror  string // used for the archive only; the checksum always comes from GitHub
}

// Release is one downloaded, verified and extracted LCU release.
type Release struct {
	Version string
	Dir     string // the extracted release, holding scripts/install.sh
	Work    string // the directory to remove once the release is installed
}

// Fetch downloads a release into a new directory under parent, checks the
// archive against the SHA-256 published with it, and extracts it.
func Fetch(d Download, parent string) (Release, error) {
	target, err := Target()
	if err != nil {
		return Release{}, err
	}
	c, err := client(d.Proxy)
	if err != nil {
		return Release{}, err
	}
	version := strings.TrimPrefix(d.Version, "v")
	if version == "" {
		if version, err = latest(c); err != nil {
			return Release{}, err
		}
	}
	if !versionRE.MatchString(version) {
		return Release{}, fmt.Errorf("版本号不对：%q", version)
	}
	name := fmt.Sprintf("lcu-%s-%s.tar.gz", version, target)
	base := githubURL + "/" + Repo + "/releases/download/v" + version + "/"
	want, err := checksum(c, base+name+".sha256", name)
	if err != nil {
		return Release{}, err
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return Release{}, err
	}
	work, err := os.MkdirTemp(parent, "lcu-"+version+"-")
	if err != nil {
		return Release{}, err
	}
	fail := func(err error) (Release, error) {
		os.RemoveAll(work)
		return Release{}, err
	}
	archive := filepath.Join(work, name)
	if err := download(c, mirrored(d.Mirror, base+name), archive, want); err != nil {
		return fail(err)
	}
	// The archive's own symlinks point into the installed prefix, so they are
	// left to tar rather than resolved here.
	if out, err := exec.Command("tar", "-xzf", archive, "-C", work).CombinedOutput(); err != nil {
		return fail(fmt.Errorf("解压 %s：%v %s", name, err, strings.TrimSpace(string(out))))
	}
	os.Remove(archive)
	dir := filepath.Join(work, strings.TrimSuffix(name, ".tar.gz"))
	if !fileExists(filepath.Join(dir, "scripts", "install.sh")) {
		return fail(fmt.Errorf("%s 里没有 scripts/install.sh", name))
	}
	return Release{Version: version, Dir: dir, Work: work}, nil
}

// InstallRuntime runs a fetched release's installer without registering any
// harness; registration goes through lcu setup against the main Claude home.
func InstallRuntime(r Release, existingApp string, env []string) error {
	args := []string{"--runtime-only"}
	if existingApp != "" {
		args = append(args, "--existing-app", existingApp)
	}
	return Run(filepath.Join(r.Dir, "scripts", "install.sh"), args, env)
}

func client(proxy string) (*http.Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if proxy != "" {
		u, err := url.Parse(proxy)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("代理地址不对：%s", proxy)
		}
		tr.Proxy = http.ProxyURL(u)
	}
	return &http.Client{Transport: tr, Timeout: 10 * time.Minute}, nil
}

// latest reads the tag from the redirect of /releases/latest, which is not
// subject to the GitHub API rate limit.
func latest(c *http.Client) (string, error) {
	nc := *c
	nc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := nc.Get(githubURL + "/" + Repo + "/releases/latest")
	if err != nil {
		return "", fmt.Errorf("查 LCU 最新版本：%w", err)
	}
	resp.Body.Close()
	_, tag, ok := strings.Cut(resp.Header.Get("Location"), "/releases/tag/")
	if !ok || tag == "" {
		return "", fmt.Errorf("没拿到 LCU 最新版本号（%s）。可以用 --version 指定", resp.Status)
	}
	return strings.TrimPrefix(tag, "v"), nil
}

func checksum(c *http.Client, u, name string) (string, error) {
	resp, err := get(c, u)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	f := strings.Fields(string(b))
	if len(f) < 2 || strings.TrimPrefix(f[1], "*") != name {
		return "", fmt.Errorf("%s 不是 %s 的校验文件", u, name)
	}
	sum := strings.ToLower(f[0])
	if raw, err := hex.DecodeString(sum); err != nil || len(raw) != sha256.Size {
		return "", fmt.Errorf("%s 里的 SHA-256 格式不对", u)
	}
	return sum, nil
}

func download(c *http.Client, u, dst, want string) error {
	resp, err := get(c, u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("下载 %s：%w", u, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		os.Remove(dst)
		return fmt.Errorf("%s 校验失败：应为 %s，实际 %s", filepath.Base(dst), want, got)
	}
	return nil
}

func get(c *http.Client, u string) (*http.Response, error) {
	resp, err := c.Get(u)
	if err != nil {
		return nil, fmt.Errorf("下载 %s：%w", u, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("下载 %s：%s", u, resp.Status)
	}
	return resp, nil
}

func mirrored(mirror, u string) string {
	if mirror == "" {
		return u
	}
	return strings.TrimSuffix(mirror, "/") + "/" + u
}
