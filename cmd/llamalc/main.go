package main

import (
	"os"

	"github.com/Snail-one/LlamaLc/internal/launcher"
	"github.com/Snail-one/LlamaLc/internal/llama"
)

func main() { os.Exit(launcher.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, llama.OSExecutor{})) }
