package main

import (
	"os"

	"github.com/hxmbl/git-sync/internal/app"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(app.Run(os.Args[1:], os.Stdout, os.Stderr, version))
}
