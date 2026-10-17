// Command magenta manages mgnt's rules, colorschemes, and settings.
package main

import (
	"os"

	"github.com/styd/magenta/internal/magentacli"
)

func main() { os.Exit(magentacli.Run(magentacli.DefaultEnv(), os.Args[1:])) }
