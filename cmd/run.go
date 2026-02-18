package cmd

import (
	"time"

	"github.com/charmbracelet/log"
	"github.com/spf13/cobra"

	"github.com/sol-strategies/solana-validator-snapshot-keeper/internal/manager"
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Run the snapshot keeper (once or on an interval)",
	RunE: func(cmd *cobra.Command, args []string) error {
		intervalStr, _ := cmd.Flags().GetString("on-interval")

		m := manager.New(cfg)

		if intervalStr != "" {
			duration, err := time.ParseDuration(intervalStr)
			if err != nil {
				log.Fatal("invalid interval", "value", intervalStr, "error", err)
			}
			return m.RunOnInterval(duration)
		}

		return m.RunOnce()
	},
}

func init() {
	runCmd.Flags().StringP("on-interval", "i", "", "run on an interval (e.g. 4h, 30m)")
	rootCmd.AddCommand(runCmd)
}
