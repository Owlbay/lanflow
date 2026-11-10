package i18n

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestNoDuplicateKeys fails when a key is given an English text in more
// than one place of the en*.go tables: the init order decides which one
// wins, as a 「关闭」 meaning Off once hid the dialog's Close. A word with
// two meanings takes a context ("关闭|开关").
func TestNoDuplicateKeys(t *testing.T) {
	files, err := filepath.Glob("en*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no catalog files: %v", err)
	}
	fset := token.NewFileSet()
	where := map[string][]string{}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			if mt, ok := cl.Type.(*ast.MapType); !ok || !isString(mt.Key) || !isString(mt.Value) {
				return true
			}
			for _, el := range cl.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				lit, ok := kv.Key.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				k, err := strconv.Unquote(lit.Value)
				if err != nil {
					continue
				}
				p := fset.Position(lit.Pos())
				where[k] = append(where[k], p.Filename+":"+strconv.Itoa(p.Line))
			}
			return true
		})
	}
	var dups []string
	for k, at := range where {
		if len(at) > 1 {
			dups = append(dups, strconv.Quote(k)+" at "+strings.Join(at, ", "))
		}
	}
	sort.Strings(dups)
	for _, d := range dups {
		t.Errorf("key given more than once: %s", d)
	}
}

func isString(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "string"
}

// TestCloseAndOff: the button says Close, a setting's value says Off.
func TestCloseAndOff(t *testing.T) {
	t.Cleanup(func() { SetLang(ZH) })
	SetLang(EN)
	if T("关闭") != "Close" || T("关闭|开关") != "Off" {
		t.Fatalf("关闭 %q, 关闭|开关 %q", T("关闭"), T("关闭|开关"))
	}
	SetLang(ZH)
	if T("关闭|开关") != "关闭" {
		t.Fatal("the context must not show in Chinese")
	}
}
