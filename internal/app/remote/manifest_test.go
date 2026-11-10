package remote

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"lanflow/internal/app"
	"lanflow/internal/app/remote/apigen"
	"lanflow/internal/rpc"
)

// TestManifestCoversAPI: every app.API method is in the manifest with a
// permission level. A method added to the API without an entry in
// internal/rpc/levels.go fails here (and in go generate).
func TestManifestCoversAPI(t *testing.T) {
	valid := map[rpc.Level]bool{rpc.LevelRead: true, rpc.LevelSensitive: true, rpc.LevelWrite: true, rpc.LevelDanger: true, rpc.LevelUI: true}
	api := reflect.TypeOf((*app.API)(nil)).Elem()
	for i := 0; i < api.NumMethod(); i++ {
		name := api.Method(i).Name
		if l := rpc.LevelOf(name); !valid[l] {
			t.Errorf("app.API.%s has no permission level in internal/rpc/levels.go", name)
		}
		m, ok := rpc.Lookup(name)
		if !ok {
			t.Errorf("app.API.%s is missing from the manifest: run go generate ./internal/app/remote", name)
			continue
		}
		if !valid[m.Level] {
			t.Errorf("manifest entry %s has level %q", name, m.Level)
		}
		if m.Agent {
			t.Errorf("%s is marked as an agent-only method", name)
		}
		if got, want := len(m.Params), api.Method(i).Type.NumIn(); got != want {
			t.Errorf("%s: manifest has %d params, API %d", name, got, want)
		}
	}
}

// TestGeneratedUpToDate: the checked-in files match the generator.
func TestGeneratedUpToDate(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	manifest, client, err := apigen.Generate(root)
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string][]byte{apigen.Files.Manifest: manifest, apigen.Files.Client: client} {
		got, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is stale: run go generate ./internal/app/remote", path)
		}
	}
}
