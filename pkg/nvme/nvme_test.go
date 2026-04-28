package nvme

import (
	"testing"
)

func TestIsPCIAddress(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"0000:3b:00.0", true},
		{"0000:00:1f.3", true},
		{"0000:b5:00.0", true},
		{"nvme0", false},
		{"virtio0", false},
		{"", false},
		{"0000:3b:00", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := isPCIAddress(tt.input)
			if got != tt.want {
				t.Errorf("isPCIAddress(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestSanitize(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"SAMSUNG MZQL21T9HCJR-00A07", "SAMSUNG_MZQL21T9HCJR-00A07"},
		{"normal", "normal"},
		{"with (parens)", "with_parens"},
		{"with\ttab", "withtab"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitize(tt.input)
			if got != tt.want {
				t.Errorf("sanitize(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestReadIntFile(t *testing.T) {
	got := readIntFile("/nonexistent/path", -1)
	if got != -1 {
		t.Errorf("readIntFile(nonexistent) = %d, want -1", got)
	}
}

func TestReadStringFile(t *testing.T) {
	got := readStringFile("/nonexistent/path")
	if got != "" {
		t.Errorf("readStringFile(nonexistent) = %q, want \"\"", got)
	}
}
