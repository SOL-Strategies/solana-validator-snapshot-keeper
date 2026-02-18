package config

import (
	"testing"
)

func TestParseSize(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
		wantErr  bool
	}{
		{"60mb", 60 * 1024 * 1024, false},
		{"60MB", 60 * 1024 * 1024, false},
		{"60Mb", 60 * 1024 * 1024, false},
		{"100kb", 100 * 1024, false},
		{"100KB", 100 * 1024, false},
		{"1gb", 1024 * 1024 * 1024, false},
		{"1GB", 1024 * 1024 * 1024, false},
		{"1tb", 1024 * 1024 * 1024 * 1024, false},
		{"512b", 512, false},
		{"1.5gb", 1.5 * 1024 * 1024 * 1024, false},
		{"  60mb  ", 60 * 1024 * 1024, false},
		{"60 mb", 60 * 1024 * 1024, false},
		{"", 0, true},
		{"abc", 0, true},
		{"mb", 0, true},
		{"-1mb", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseSize(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseSize(%q) expected error, got %d", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSize(%q) unexpected error: %v", tt.input, err)
			}
			if got != tt.expected {
				t.Errorf("ParseSize(%q) = %d, want %d", tt.input, got, tt.expected)
			}
		})
	}
}

func TestFormatSize(t *testing.T) {
	tests := []struct {
		bytes    int64
		expected string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1024 * 1024, "1.0 MB"},
		{60 * 1024 * 1024, "60.0 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
		{int64(1.5 * 1024 * 1024 * 1024), "1.5 GB"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			got := FormatSize(tt.bytes)
			if got != tt.expected {
				t.Errorf("FormatSize(%d) = %q, want %q", tt.bytes, got, tt.expected)
			}
		})
	}
}
