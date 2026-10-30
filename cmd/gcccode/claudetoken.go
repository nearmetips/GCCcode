package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nearmetips/DailiCode/internal/bins"
	"github.com/nearmetips/DailiCode/internal/browser"
	"github.com/nearmetips/DailiCode/internal/claude"
	"github.com/nearmetips/DailiCode/internal/core"
	"github.com/nearmetips/DailiCode/internal/setuptoken"
)

var stdin = bufio.NewReader(os.Stdin)

func readLine() string {
	line, _ := stdin.ReadString('\n')
	return strings.TrimSpace(line)
}

// pasted delivers stdin lines from a background reader. It must only start
// once no child shares the terminal, or it would steal the child's input.
var pasted = sync.OnceValue(func() <-chan string {
	ch := make(chan string)
	go func() {
		for {
			line, err := stdin.ReadString('\n')
			if line != "" {
				ch <- line
			}
			if err != nil {
				close(ch)
				return
			}
		}
	}()
	return ch
})

// readPastedToken takes one paste. A terminal that wrapped the token hands
// it over as several lines in one burst, so lines arriving right after the
// first are joined to it.
func readPastedToken() string {
	lines := pasted()
	var b strings.Builder
	for {
		var line string
		var ok bool
		if b.Len() == 0 {
			line, ok = <-lines
		} else {
			select {
			case line, ok = <-lines:
			case <-time.After(300 * time.Millisecond):
				return claude.CleanToken(b.String())
			}
		}
		if !ok {
			return claude.CleanToken(b.String())
		}
		b.WriteString(line)
	}
}

func isURL(s string) bool { return strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://") }

// openAccountBrowser shows url in the browser only account id signs in with.
func openAccountBrowser(id, url string) error {
	proxy := os.Getenv(setuptoken.ProxyEnv)
	if st, err := core.Load(); err == nil {
		if a, ok := st.Account(id); ok {
			proxy = claude.EffectiveProxy(st.Settings, *a)
		}
	}
	err := browser.Open(id, url, proxy)
	if errors.Is(err, browser.ErrNone) {
		return fmt.Errorf("%w。请在浏览器里新建一个个人资料，在那个窗口打开：%s", err, url)
	}
	return err
}

// addClaudeToken adds a Claude account that signs in with a long-lived token
// from `claude setup-token`, authorized in a browser only this account uses.
func addClaudeToken(label string) error {
	st, err := core.Load()
	if err != nil {
		return err
	}
	if _, err := bins.Claude(st.Settings); err != nil {
		return fmt.Errorf("找不到 claude，先安装 Claude Code：%w", err)
	}
	fmt.Println("添加 Claude 会员号。每个号用自己的长期 token 登录，换号就是换 token。")
	if label == "" {
		fmt.Print("给这个号起个名字（比如它的邮箱），直接回车就用编号：")
		label = readLine()
	}
	proxy := ""
	if others := st.ByKind(core.KindClaude); len(others) > 0 {
		fmt.Printf("你已经有 %d 个 Claude 号。多个号走同一个出口 IP 会被认成一组，这个号最好单独走一个代理。\n", len(others))
		for {
			fmt.Print("这个号的代理（例如 http://127.0.0.1:7891，没有就直接回车）：")
			p := readLine()
			if p == "" {
				break
			}
			if msg := claude.ProxyProblem(p); msg != "" {
				fmt.Println(msg)
				continue
			}
			proxy = p
			break
		}
	}
	acc := setuptoken.NewAccount(st, label, proxy)
	tok, err := runSetupToken(st.Settings, acc)
	if err != nil {
		setuptoken.Discard(acc.ID)
		return err
	}
	if err := setuptoken.Save(acc, tok); err != nil {
		return err
	}
	fmt.Printf("已加入号池：%s。token 大约 %d 天后过期，快到期时 gcccode 会提醒你续期。\n", acc.Label, int(claude.TokenLifetime.Hours()/24))
	return nil
}

// tokenAccount replaces an account's token. With no value it walks the user
// through `claude setup-token` again; "-" reads the token from stdin.
func tokenAccount(id, value string) error {
	st, err := core.Load()
	if err != nil {
		return err
	}
	a, ok := st.Account(id)
	if !ok {
		return fmt.Errorf("没有这个账号：%s", id)
	}
	if a.Kind != core.KindClaude {
		return fmt.Errorf("只有 Claude 号用长期 token")
	}
	var tok string
	switch value {
	case "":
		if tok, err = runSetupToken(st.Settings, *a); err != nil {
			return err
		}
	case "-":
		raw, err := io.ReadAll(io.LimitReader(os.Stdin, 64<<10))
		if err != nil {
			return err
		}
		tok = claude.CleanToken(string(raw))
	default:
		tok = claude.CleanToken(value)
	}
	return saveToken(id, tok)
}

func saveToken(id, tok string) error {
	if err := claude.SaveToken(id, tok); err != nil {
		return err
	}
	name := id
	_, err := core.Mutate(func(s *core.Store) error {
		a, ok := s.Account(id)
		if !ok {
			return fmt.Errorf("没有这个账号：%s", id)
		}
		if a.Label != "" {
			name = a.Label
		}
		a.TokenAt = time.Now()
		a.Quarantined = false
		a.QuarantineReason = ""
		return nil
	})
	if err == nil {
		fmt.Printf("%s 的 token 已换上，大约 %d 天后过期。\n", name, int(claude.TokenLifetime.Hours()/24))
	}
	return err
}

// runSetupToken runs `claude setup-token` for acc on this terminal and asks
// for the token it prints. Its sign-in page opens in acc's own browser:
// authorizing in a browser already signed in to another account would hand
// back that account's token.
func runSetupToken(s core.Settings, acc core.Account) (string, error) {
	own := browser.Available()
	fmt.Println()
	if own {
		fmt.Println("接下来会打开一个只给这个号用的浏览器，和你平时用的完全分开，登录状态不会串号。")
		fmt.Println("  1. 在这个专用浏览器里登录这个 Claude Pro 号，然后点 Authorize（授权）。")
		fmt.Println("  2. 用邮箱登录的话，邮件里的链接要复制到这个专用浏览器里打开，别直接点。")
	} else {
		fmt.Println("没找到 Chrome、Edge 或 Brave，没法自动打开专用浏览器。请先在浏览器里新建一个个人资料")
		fmt.Println("（Chrome：右上角头像 → 添加），下面会出现一个链接，复制到那个新资料的窗口里打开，")
		fmt.Println("在那里登录这个 Claude Pro 号再授权。别在登着别的 Claude 号的浏览器里授权，否则拿到的是那个号的 token。")
	}
	fmt.Print("准备好了按回车……")
	readLine()
	cmd, err := setuptoken.Command(context.Background(), s, acc)
	if err != nil {
		return "", err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	runErr := cmd.Run()
	fmt.Println()
	fmt.Println("把上面打印出来的 token（sk-ant-oat01- 开头的一长串）粘贴到这里，然后回车：")
	for try := 0; ; try++ {
		tok := readPastedToken()
		msg := claude.TokenProblem(tok)
		if msg == "" {
			return tok, nil
		}
		if try == 2 || tok == "" && runErr != nil {
			if runErr != nil {
				return "", fmt.Errorf("%s（claude setup-token：%v）", msg, runErr)
			}
			return "", errors.New(msg)
		}
		fmt.Println(msg)
		fmt.Println("再粘贴一次：")
	}
}
