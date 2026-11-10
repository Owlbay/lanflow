package i18n

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// dataLiterals are Chinese string literals that are data in the files
// named, not user-visible text: values compared or stored, never shown as
// such. Everything else with a CJK character must have an English entry.
var dataLiterals = map[string]string{
	// model.CoreDownload.Phase values, which env.CoreDownloadText switches
	// on and words itself.
	"下载": "coredl.go env.go", "校验": "coredl.go env.go", "安装": "coredl.go env.go", "启动": "coredl.go env.go",
	// A default site name: user data.
	"百度": "settings.go",
	// Parts of the core's exit message (internal/core/runner.go) that
	// errlog.go looks for.
	"最近日志：": "errlog.go", "退出码 0": "errlog.go", "，, \n": "errlog.go",
}

// hasCJK reports whether s holds a Han character or CJK/fullwidth
// punctuation.
func hasCJK(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || (r >= 0x3000 && r <= 0x303F) || (r >= 0xFF00 && r <= 0xFFEF) {
			return true
		}
	}
	return false
}

// sourceKeys collects every Chinese string literal of the user-facing
// packages: internal/view/**, internal/app (not the fake backend, whose
// Chinese is sample data), internal/syncfile (lanflow.yaml's errors), the
// main package, and the path notes of
// internal/locate/analyze.go and direct.go (the rest of locate holds ISP
// names: data).
func sourceKeys(t *testing.T) map[string][]string {
	t.Helper()
	root := filepath.Join("..", "..")
	var files []string
	for _, dir := range []string{"internal/view", "internal/app", "internal/syncfile"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "fake" {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"main.go", "main_service.go", filepath.Join("internal", "locate", "analyze.go"), filepath.Join("internal", "locate", "direct.go")} {
		if _, err := os.Stat(filepath.Join(root, f)); err == nil {
			files = append(files, filepath.Join(root, f))
		}
	}
	keys := map[string][]string{}
	fset := token.NewFileSet()
	for _, path := range files {
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		rel, _ := filepath.Rel(root, path)
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil || !hasCJK(s) || strings.Contains(dataLiterals[s], filepath.Base(path)) {
				return true
			}
			pos := fset.Position(lit.Pos())
			keys[s] = append(keys[s], rel+":"+strconv.Itoa(pos.Line))
			return true
		})
	}
	return keys
}

// TestEveryKeyHasEnglish fails when a Chinese string of the sources has no
// English entry, and when an entry translates nothing in the sources.
func TestEveryKeyHasEnglish(t *testing.T) {
	keys := sourceKeys(t)
	if len(keys) < 500 {
		t.Fatalf("found only %d Chinese strings; is the scan looking at the right tree?", len(keys))
	}
	var missing []string
	for k, where := range keys {
		if !Has(k) {
			missing = append(missing, strconv.Quote(k)+"  // "+where[0])
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("no English entry for %s", m)
	}
	var unused []string
	for _, k := range Keys() {
		if _, ok := keys[k]; !ok {
			unused = append(unused, strconv.Quote(k))
		}
	}
	sort.Strings(unused)
	for _, u := range unused {
		t.Errorf("English entry %s is used nowhere", u)
	}
}

// TestEnglishHasNoCJK keeps the English table English.
func TestEnglishHasNoCJK(t *testing.T) {
	for k, v := range en {
		if hasCJK(v) {
			t.Errorf("English of %q contains CJK text: %q", k, v)
		}
		if strings.TrimSpace(v) == "" {
			t.Errorf("English of %q is empty", k)
		}
	}
}

// TestFormatVerbsMatch checks that a translation keeps the format verbs of
// its key, in order, so Tf and fmt.Errorf get what they expect.
func TestFormatVerbsMatch(t *testing.T) {
	for k, v := range en {
		if kv, vv := verbs(k), verbs(v); kv != vv {
			t.Errorf("format verbs differ for %q: key has %q, English has %q", k, kv, vv)
		}
	}
}

func verbs(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '%' || i+1 >= len(s) {
			continue
		}
		i++
		if s[i] == '%' {
			continue
		}
		b.WriteByte('%')
		b.WriteByte(s[i])
	}
	return b.String()
}

func TestLangAndNormalize(t *testing.T) {
	t.Cleanup(func() { SetLang(ZH) })
	for in, want := range map[string]string{"": ZH, "zh": ZH, "zh-Hans-CN": ZH, "en": EN, "en-GB": EN, "English": EN, "fr": ZH} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
	for _, c := range []struct {
		pref []string
		want string
	}{
		{nil, ZH}, {[]string{"zh-Hans-CN", "en"}, ZH}, {[]string{"zh-TW"}, ZH}, {[]string{"en-US", "zh"}, EN}, {[]string{"de-DE"}, EN},
	} {
		if got := FromSystem(c.pref); got != c.want {
			t.Errorf("FromSystem(%q) = %q, want %q", c.pref, got, c.want)
		}
	}
	if Tag(ZH) != "zh-Hans" || Tag(EN) != "en" || Autonym(ZH) != "简体中文" || Autonym(EN) != "English" {
		t.Error("Tag or Autonym wrong")
	}
	SetLang(EN)
	if T("取消") != "Cancel" || Tf("%d 分钟前", 5) != "5 min ago" || Error("取消").Error() != "Cancel" {
		t.Errorf("English lookup: %q %q", T("取消"), Tf("%d 分钟前", 5))
	}
	if T("not a key") != "not a key" {
		t.Error("unknown keys must come back unchanged")
	}
	if T("规则|页") != "Rules" || T("规则") != "Rule" {
		t.Errorf("context keys: %q %q", T("规则|页"), T("规则"))
	}
	if KeyOf("Cancel", []string{"取消", "好"}) != "取消" || Options("取消", "好")[1] != "OK" {
		t.Error("Options/KeyOf")
	}
	SetLang(ZH)
	if T("取消") != "取消" || Error("取消").Error() != "取消" {
		t.Error("Chinese must return the key")
	}
	if T("规则|页") != "规则" || T("没有的键|上下文") != "没有的键" {
		t.Error("Chinese must drop the context of a key")
	}
}
