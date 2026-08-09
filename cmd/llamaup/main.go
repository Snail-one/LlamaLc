package main

import (
	"os"

	"github.com/joker/llama-launcher/internal/updater"
)

func main() {
	os.Exit(updater.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
