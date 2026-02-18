package config

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/log"
)

var (
	logFormatters = map[string]log.Formatter{
		"text":   log.TextFormatter,
		"json":   log.JSONFormatter,
		"logfmt": log.LogfmtFormatter,
	}
)

// Log represents the logging configuration
type Log struct {
	// Level is the log level - one of "debug", "info", "warn", "error", "fatal", defaults to "info", overwritable by --log-level command line flag
	Level string `koanf:"level"`
	// Format is the log format - one of "text" or "json" or "logfmt", defaults to text
	Format string `koanf:"format"`
	// DisableTimestamps turns off timestamps in log output; default false, overridden by --log-disable-timestamps
	DisableTimestamps bool `koanf:"disable_timestamps"`
	// ParsedLevel is the parsed log level
	ParsedLevel log.Level `koanf:"-"`
	// ParsedFormat is the parsed log format
	ParsedFormatter log.Formatter `koanf:"-"`
}

// SetDefaults sets default values for the log configuration
func (l *Log) SetDefaults() {
	if l.Level == "" {
		l.Level = "info"
	}
	if l.Format == "" {
		l.Format = "text"
	}
}

// Validate validates the log configuration
func (l *Log) Validate() (err error) {
	l.ParsedLevel, err = log.ParseLevel(l.Level)
	if err != nil {
		return fmt.Errorf("log.level must be one of debug, info, warn, error, fatal - got: %s", l.Level)
	}

	var ok bool
	l.ParsedFormatter, ok = logFormatters[l.Format]
	if !ok {
		return fmt.Errorf("log.format must be one of text, json, logfmt - got: %s", l.Format)
	}

	return nil
}

// SetLoggerDefaults applies time format, UTC, and styles to the global logger.
// Call this early (e.g. in cmd init()) so pre-config errors are styled correctly.
func SetLoggerDefaults() {
	log.SetTimeFunction(log.NowUTC)
	log.SetTimeFormat("2006-01-02T15:04:05.000Z07:00")

	styles := log.DefaultStyles()
	styles.Timestamp = lipgloss.NewStyle().Faint(true)
	styles.Message = lipgloss.NewStyle().Foreground(lipgloss.Color("213"))
	styles.Value = lipgloss.NewStyle().Foreground(lipgloss.Color("105"))

	styles.Levels[log.DebugLevel] = styles.Levels[log.DebugLevel].Foreground(lipgloss.Color("86"))
	styles.Levels[log.InfoLevel] = styles.Levels[log.InfoLevel].Foreground(lipgloss.Color("82"))
	styles.Levels[log.WarnLevel] = styles.Levels[log.WarnLevel].Foreground(lipgloss.Color("226"))
	styles.Levels[log.ErrorLevel] = styles.Levels[log.ErrorLevel].Foreground(lipgloss.Color("196"))
	styles.Levels[log.FatalLevel] = styles.Levels[log.FatalLevel].Foreground(lipgloss.Color("208"))

	log.SetStyles(styles)
}

// ConfigureWithLevelString configures the logger with the supplied settings.
// If logLevel is provided and different from the config level, it overrides the config.
// If disableTimestampsOverride is true, timestamps are disabled regardless of config.
func (l *Log) ConfigureWithLevelString(logLevel string, disableTimestampsOverride bool) {
	if logLevel != "" && logLevel != l.Level {
		parsedLevel, err := log.ParseLevel(logLevel)
		if err != nil {
			log.Error("invalid level, using "+l.Level, "invalid_level", logLevel, "error", err)
		} else {
			l.Level = logLevel
			l.ParsedLevel = parsedLevel
		}
	}

	log.SetLevel(l.ParsedLevel)
	log.SetFormatter(l.ParsedFormatter)

	disable := l.DisableTimestamps || disableTimestampsOverride
	log.SetReportTimestamp(!disable)

	SetLoggerDefaults()
}
