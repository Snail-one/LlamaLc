package main

import (
	"os"

	"github.com/joker/llama-launcher/internal/launcher"
)

func main() {
	os.Exit(launcher.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, launcher.OSExecutor{}))
}
