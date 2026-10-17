// Command mgnt colors logs in the terminal.
package main

import (
	"os"

	"github.com/styd/magenta/internal/mgnt"
)

func main() { os.Exit(mgnt.Run(mgnt.DefaultEnv(), os.Args[1:])) }
