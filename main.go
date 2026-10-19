package main

import (
	"os"

	"github.com/marcowarnke/bb/internal/cmd"
)

func main() {
	os.Exit(cmd.Execute())
}
