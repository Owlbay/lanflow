// Package apigen generates, from the app.API interface
// (internal/app/api.go) and the permission table (internal/rpc/levels.go):
//
//   - internal/rpc/manifest_gen.go: the method manifest (name, parameters,
//     results, permission level, doc) used by the server, ctl and MCP;
//   - internal/app/remote/client_gen.go: the remote client's methods.
//
// It fails when an API method has no level, or levels.go names a method
// the API does not have. Run it with `go generate ./internal/app/remote`.
package apigen

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/printer"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Files names the generated files relative to the repository root.
var Files = struct{ Manifest, Client string }{
	Manifest: filepath.Join("internal", "rpc", "manifest_gen.go"),
	Client:   filepath.Join("internal", "app", "remote", "client_gen.go"),
}

// handwritten are the API methods the remote client implements by hand
// (client.go): OnChange runs on the event stream, UpdateUIState calls fn
// locally and sends the result.
var handwritten = map[string]bool{"OnChange": true, "UpdateUIState": true}

type param struct{ name, typ, qtyp string }

type method struct {
	name    string
	params  []param
	results []param // without the error
	err     bool
	level   string // the constant's name, e.g. LevelRead
	doc     string
}

// Generate reads the sources under root and returns the two files.
func Generate(root string) (manifest, client []byte, err error) {
	ms, err := parseAPI(filepath.Join(root, "internal", "app", "api.go"))
	if err != nil {
		return nil, nil, err
	}
	levels, err := parseLevels(filepath.Join(root, "internal", "rpc", "levels.go"))
	if err != nil {
		return nil, nil, err
	}
	var missing, extra []string
	seen := map[string]bool{}
	for i := range ms {
		seen[ms[i].name] = true
		l, ok := levels[ms[i].name]
		if !ok {
			missing = append(missing, ms[i].name)
		}
		ms[i].level = l
	}
	for name := range levels {
		if !seen[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	if len(missing) > 0 {
		return nil, nil, fmt.Errorf("internal/rpc/levels.go: no permission level for %s", strings.Join(missing, ", "))
	}
	if len(extra) > 0 {
		return nil, nil, fmt.Errorf("internal/rpc/levels.go: %s not in app.API", strings.Join(extra, ", "))
	}
	if manifest, err = genManifest(ms); err != nil {
		return nil, nil, err
	}
	if client, err = genClient(ms); err != nil {
		return nil, nil, err
	}
	return manifest, client, nil
}

func parseAPI(path string) ([]method, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	var iface *ast.InterfaceType
	ast.Inspect(f, func(n ast.Node) bool {
		if ts, ok := n.(*ast.TypeSpec); ok && ts.Name.Name == "API" {
			iface, _ = ts.Type.(*ast.InterfaceType)
		}
		return iface == nil
	})
	if iface == nil {
		return nil, fmt.Errorf("%s: no API interface", path)
	}
	var out []method
	for _, fld := range iface.Methods.List {
		ft, ok := fld.Type.(*ast.FuncType)
		if !ok || len(fld.Names) == 0 {
			return nil, fmt.Errorf("%s: API embeds %s; list the methods instead", path, exprString(fld.Type))
		}
		m := method{name: fld.Names[0].Name, doc: docOf(fld)}
		m.params = fieldParams(ft.Params, "p")
		if ft.Results != nil {
			res := fieldParams(ft.Results, "r")
			if n := len(res); n > 0 && res[n-1].typ == "error" {
				m.err = true
				res = res[:n-1]
			}
			m.results = res
		}
		out = append(out, m)
	}
	return out, nil
}

func fieldParams(fl *ast.FieldList, prefix string) []param {
	var out []param
	if fl == nil {
		return nil
	}
	for _, f := range fl.List {
		typ, qtyp := exprString(f.Type), exprString(qualify(f.Type))
		if len(f.Names) == 0 {
			out = append(out, param{name: prefix + strconv.Itoa(len(out)), typ: typ, qtyp: qtyp})
			continue
		}
		for _, n := range f.Names {
			name := n.Name
			if name == "_" {
				name = prefix + strconv.Itoa(len(out))
			}
			out = append(out, param{name: name, typ: typ, qtyp: qtyp})
		}
	}
	return out
}

func docOf(f *ast.Field) string {
	var s string
	if f.Doc != nil {
		s = f.Doc.Text()
	}
	if f.Comment != nil {
		s = strings.TrimSpace(s + " " + f.Comment.Text())
	}
	return strings.Join(strings.Fields(s), " ")
}

var builtin = map[string]bool{
	"bool": true, "string": true, "int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true, "uintptr": true,
	"byte": true, "rune": true, "float32": true, "float64": true, "error": true, "any": true,
}

// qualify returns e with the app package's own types (Topic, Notice)
// qualified as app.X, for use outside package app.
func qualify(e ast.Expr) ast.Expr {
	switch x := e.(type) {
	case *ast.Ident:
		if !builtin[x.Name] && ast.IsExported(x.Name) {
			return &ast.SelectorExpr{X: ast.NewIdent("app"), Sel: ast.NewIdent(x.Name)}
		}
		return x
	case *ast.StarExpr:
		return &ast.StarExpr{X: qualify(x.X)}
	case *ast.ArrayType:
		return &ast.ArrayType{Len: x.Len, Elt: qualify(x.Elt)}
	case *ast.MapType:
		return &ast.MapType{Key: qualify(x.Key), Value: qualify(x.Value)}
	case *ast.Ellipsis:
		return &ast.Ellipsis{Elt: qualify(x.Elt)}
	case *ast.FuncType:
		return &ast.FuncType{Params: qualifyList(x.Params), Results: qualifyList(x.Results)}
	}
	return e
}

func qualifyList(fl *ast.FieldList) *ast.FieldList {
	if fl == nil {
		return nil
	}
	out := &ast.FieldList{}
	for _, f := range fl.List {
		out.List = append(out.List, &ast.Field{Names: f.Names, Type: qualify(f.Type)})
	}
	return out
}

func exprString(e ast.Expr) string {
	var b bytes.Buffer
	_ = printer.Fprint(&b, token.NewFileSet(), e)
	return b.String()
}

// parseLevels reads the methodLevels map literal of levels.go.
func parseLevels(path string) (map[string]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "methodLevels" || len(vs.Values) != 1 {
			return true
		}
		cl, ok := vs.Values[0].(*ast.CompositeLit)
		if !ok {
			return false
		}
		found = true
		for _, el := range cl.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			k, ok1 := kv.Key.(*ast.BasicLit)
			v, ok2 := kv.Value.(*ast.Ident)
			if !ok1 || !ok2 {
				continue
			}
			name, err := strconv.Unquote(k.Value)
			if err == nil {
				out[name] = v.Name
			}
		}
		return false
	})
	if !found {
		return nil, fmt.Errorf("%s: no methodLevels map", path)
	}
	return out, nil
}

const header = "// Code generated by internal/app/remote/apigen from internal/app/api.go and internal/rpc/levels.go; DO NOT EDIT.\n\n"

func genManifest(ms []method) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(header)
	b.WriteString("package rpc\n\n// apiMethods is the manifest of app.API, in declaration order.\nvar apiMethods = []Method{\n")
	for _, m := range ms {
		fmt.Fprintf(&b, "\t{Name: %q", m.name)
		if len(m.params) > 0 {
			b.WriteString(", Params: []Param{")
			for i, p := range m.params {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(&b, "{Name: %q, Type: %q}", p.name, p.typ)
			}
			b.WriteString("}")
		}
		if len(m.results) > 0 {
			b.WriteString(", Results: []string{")
			for i, r := range m.results {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(&b, "%q", r.typ)
			}
			b.WriteString("}")
		}
		if m.err {
			b.WriteString(", Error: true")
		}
		fmt.Fprintf(&b, ", Level: %s", m.level)
		if m.doc != "" {
			fmt.Fprintf(&b, ", Doc: %q", m.doc)
		}
		b.WriteString("},\n")
	}
	b.WriteString("}\n")
	return format.Source(b.Bytes())
}

func genClient(ms []method) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(header)
	b.WriteString("package remote\n\nimport (\n\t\"lanflow/internal/app\"\n\t\"lanflow/internal/model\"\n)\n\n")
	b.WriteString("var _ app.API = (*Client)(nil)\n\nvar _ model.Status\n")
	for _, m := range ms {
		if handwritten[m.name] {
			continue
		}
		var ps, args []string
		for _, p := range m.params {
			ps = append(ps, p.name+" "+p.qtyp)
			args = append(args, p.name)
		}
		var rs, outs []string
		for i, r := range m.results {
			n := "res" + strconv.Itoa(i)
			rs = append(rs, n+" "+r.qtyp)
			outs = append(outs, "&"+n)
		}
		if m.err {
			rs = append(rs, "err error")
		}
		argList := "nil"
		if len(args) > 0 {
			argList = "[]any{" + strings.Join(args, ", ") + "}"
		}
		call := fmt.Sprintf("cl.call(%q, %s", m.name, argList)
		if len(outs) > 0 {
			call += ", " + strings.Join(outs, ", ")
		}
		call += ")"
		fmt.Fprintf(&b, "\nfunc (cl *Client) %s(%s)", m.name, strings.Join(ps, ", "))
		if len(rs) > 0 {
			fmt.Fprintf(&b, " (%s)", strings.Join(rs, ", "))
		}
		b.WriteString(" {\n")
		switch {
		case m.err:
			fmt.Fprintf(&b, "\terr = %s\n\treturn\n", call)
		case len(rs) > 0:
			fmt.Fprintf(&b, "\t_ = %s\n\treturn\n", call)
		default:
			fmt.Fprintf(&b, "\t_ = %s\n", call)
		}
		b.WriteString("}\n")
	}
	return format.Source(b.Bytes())
}
