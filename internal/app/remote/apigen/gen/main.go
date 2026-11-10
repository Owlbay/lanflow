// Command gen writes the generated files of package apigen; run by
// `go generate ./internal/app/remote`.
package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"

	"lanflow/internal/app/remote/apigen"
)

func main() {
	root := flag.String("root", filepath.Join("..", "..", ".."), "repository root")
	flag.Parse()
	manifest, client, err := apigen.Generate(*root)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(*root, apigen.Files.Manifest), manifest, 0o644); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(*root, apigen.Files.Client), client, 0o644); err != nil {
		log.Fatal(err)
	}
}
