package main

import (
	"os"

	"github.com/sol-strategies/solana-validator-snapshot-keeper/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
