package main

import (
	"os"

	"github.com/gliedabrennung/halyk-agent/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
