package cmd

import (
	_ "embed"
	"strings"

	"github.com/charmbracelet/log"
	"github.com/spf13/cobra"

	"github.com/sol-strategies/solana-validator-snapshot-keeper/internal/config"
)

//go:embed version.txt
var version string

var cfg *config.Config

var rootCmd = &cobra.Command{
	Use:           "solana-validator-snapshot-keeper",
	Short:         "Keeps fresh Solana snapshots on disk",
	Version:       strings.TrimSpace(version),
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		configPath, _ := cmd.Flags().GetString("config")
		logLevel, _ := cmd.Flags().GetString("log-level")
		logDisableTimestamps, _ := cmd.Flags().GetBool("log-disable-timestamps")

		var err error
		cfg, err = config.NewFromConfigFile(configPath)
		if err != nil {
			return err
		}

		cfg.Log.ConfigureWithLevelString(logLevel, logDisableTimestamps)
		return nil
	},
}

func init() {
	// Set logger defaults early so any errors before config loading are styled correctly.
	config.SetLoggerDefaults()

	rootCmd.PersistentFlags().StringP("config", "c", config.DefaultConfigPath(), "path to config file")
	rootCmd.PersistentFlags().String("log-level", "", "override log level (debug, info, warn, error)")
	rootCmd.PersistentFlags().Bool("log-disable-timestamps", false, "disable timestamps in log output (overrides log.disable_timestamps)")
}

func Execute() error {
	if err := rootCmd.Execute(); err != nil {
		log.Fatal("failed to execute", "error", err)
		return err
	}
	return nil
}
