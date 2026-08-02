package main

import "github.com/nlink-jp/sensor-lens/cmd"

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	cmd.Execute(version)
}
