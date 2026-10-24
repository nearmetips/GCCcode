//go:build nogui

package tray

import "fmt"

// Run is the menu-bar app. This build was compiled without it.
func Run() error {
	return fmt.Errorf("这个构建没有菜单栏（使用了 -tags nogui）。运行 gcccode web 打开面板，或去掉该标签重新编译")
}
