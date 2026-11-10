package apigen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMissingLevelFails: a method added to app.API without a permission
// level stops the generator.
func TestMissingLevelFails(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	api, err := os.ReadFile(filepath.Join(root, "internal", "app", "api.go"))
	if err != nil {
		t.Fatal(err)
	}
	levels, err := os.ReadFile(filepath.Join(root, "internal", "rpc", "levels.go"))
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	src := strings.Replace(string(api), "\tShutdown()\n}", "\tShutdown()\n\tBrandNew(x int) error\n}", 1)
	for path, data := range map[string]string{
		filepath.Join(tmp, "internal", "app", "api.go"):    src,
		filepath.Join(tmp, "internal", "rpc", "levels.go"): string(levels),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err = Generate(tmp)
	if err == nil || !strings.Contains(err.Error(), "BrandNew") {
		t.Fatalf("err = %v, want a missing level for BrandNew", err)
	}
}
