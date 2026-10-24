package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nearmetips/DailiCode/internal/agy"
	"github.com/nearmetips/DailiCode/internal/core"
)

func TestAgyImportsTheMachinesLoginAndRefusesASecond(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GCCCODE_HOME", root)
	home := filepath.Join(root, "home")
	b, _ := json.Marshal(map[string]any{"agy_home": home})
	os.WriteFile(filepath.Join(root, "settings.json"), b, 0o600)
	if err := importAgy(""); err == nil {
		t.Fatal("imported a home with no agy login")
	}
	os.MkdirAll(filepath.Join(home, ".gemini"), 0o700)
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"me@x.com","sub":"9"}`))
	os.WriteFile(agy.TokenPath(home), []byte(`{"token":{"refresh_token":"rt"},"id_token":"h.`+payload+`.s"}`), 0o600)

	if err := addAgy(""); err != nil {
		t.Fatal(err)
	}
	st, _ := core.Load()
	accs := st.ByKind(core.KindAgy)
	if len(accs) != 1 || accs[0].Email != "me@x.com" || accs[0].ConfigDir != home {
		t.Fatalf("agy accounts %+v", accs)
	}
	err := addAgy("second")
	if err == nil || !strings.Contains(err.Error(), "暂时只能放这一个号") {
		t.Fatalf("a second agy account must be refused: %v", err)
	}
	if err := importAgy(""); err != nil {
		t.Fatal(err)
	}
	if st, _ := core.Load(); len(st.ByKind(core.KindAgy)) != 1 {
		t.Fatal("importing again duplicated the account")
	}
}
