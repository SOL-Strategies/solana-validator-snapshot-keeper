package config

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var sizeRe = regexp.MustCompile(`(?i)^\s*(\d+(?:\.\d+)?)\s*(b|kb|mb|gb|tb)?\s*$`)

// ParseSize parses a human-readable size string (e.g. "60mb", "100kb", "1.5gb")
// into bytes. If no unit is provided, bytes are assumed.
func ParseSize(s string) (int64, error) {
	matches := sizeRe.FindStringSubmatch(s)
	if matches == nil {
		return 0, fmt.Errorf("invalid size %q (expected format: \"60mb\", \"100kb\", \"1gb\")", s)
	}

	val, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size number %q: %w", matches[1], err)
	}

	unit := strings.ToLower(matches[2])
	switch unit {
	case "tb":
		val *= 1024 * 1024 * 1024 * 1024
	case "gb":
		val *= 1024 * 1024 * 1024
	case "mb", "":
		val *= 1024 * 1024
	case "kb":
		val *= 1024
	case "b":
		// already in bytes
	}

	return int64(val), nil
}

// FormatSize formats bytes into a human-readable string.
func FormatSize(bytes int64) string {
	switch {
	case bytes >= 1024*1024*1024:
		return fmt.Sprintf("%.1f GB", float64(bytes)/(1024*1024*1024))
	case bytes >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
	case bytes >= 1024:
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
