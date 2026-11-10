// Command lanflow-helper is the privileged service that runs Mihomo for
// TUN mode. See internal/platform/helper for the contract.
//
// Usage:
//
//	lanflow-helper run                                     service entry (launchd / SCM)
//	lanflow-helper install --core <path> --data-dir <dir> --uid <uid|sid>
//	lanflow-helper uninstall
//	lanflow-helper version
//
// install and uninstall must run elevated; the app invokes them through
// osascript (macOS) or ShellExecute "runas" (Windows).
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"lanflow/internal/platform/helper/protocol"
	"lanflow/internal/platform/helper/service"
)

// version is set at build time: -ldflags "-X main.version=1.2.3".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx := context.Background()
	var err error
	switch os.Args[1] {
	case "version", "-v", "--version":
		fmt.Printf("lanflow-helper %s (protocol %d)\n", version, protocol.Version)
		return
	case "run":
		logger := newLogger(service.LogPath)
		logger.Printf("lanflow-helper %s starting", version)
		err = service.Run(ctx, version, logger.Printf)
	case "install":
		fs := flag.NewFlagSet("install", flag.ContinueOnError)
		var o service.InstallOptions
		fs.StringVar(&o.CoreBin, "core", "", "Mihomo binary to allow")
		fs.StringVar(&o.DataDir, "data-dir", "", "user's Lanflow data directory")
		fs.StringVar(&o.UID, "uid", "", "installing user's uid (macOS) or SID (Windows)")
		if err = fs.Parse(os.Args[2:]); err != nil {
			os.Exit(2)
		}
		logger := newLogger(filepath.Join(service.LogDir, "install.log"))
		logger.Printf("install %s core=%s dataDir=%s uid=%s", version, o.CoreBin, o.DataDir, o.UID)
		if err = service.Install(ctx, o, logger.Printf); err != nil {
			logger.Printf("install failed: %v", err)
		} else {
			logger.Printf("install ok")
		}
	case "uninstall":
		logger := newLogger(filepath.Join(service.LogDir, "install.log"))
		logger.Printf("uninstall %s", version)
		if err = service.Uninstall(ctx, logger.Printf); err != nil {
			logger.Printf("uninstall failed: %v", err)
		} else {
			logger.Printf("uninstall ok")
		}
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "lanflow-helper:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: lanflow-helper run | install --core <path> --data-dir <dir> --uid <uid> | uninstall | version")
}

// newLogger logs to stderr and to path. Under launchd stderr already is
// LogPath, so the file is skipped there to avoid duplicate lines; under the
// Windows SCM there is no console, so the file is the only output.
func newLogger(path string) *log.Logger {
	writers := []io.Writer{os.Stderr}
	if fi, err := os.Stderr.Stat(); err == nil && fi.Mode().IsRegular() && path == service.LogPath {
		return log.New(os.Stderr, "", log.LstdFlags)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
		if f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644); err == nil {
			writers = append(writers, f)
		}
	}
	return log.New(io.MultiWriter(writers...), "", log.LstdFlags)
}
