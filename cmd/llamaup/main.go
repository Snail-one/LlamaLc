package main

import (
	"os"

	"github.com/Snail-one/LlamaLc/internal/updater"
)

func main() {
	os.Exit(updater.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
