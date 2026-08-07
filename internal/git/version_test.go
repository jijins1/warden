package git

import (
	"testing"
)

// TestParseVersion tests the git version parser.
func TestParseVersion(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		want    Version
		wantErr bool
	}{
		{
			name:   "standard version",
			output: "git version 2.45.1",
			want:   Version{Major: 2, Minor: 45, Patch: 1},
		},
		{
			name:   "version with extra metadata",
			output: "git version 2.45.1.windows.1",
			want:   Version{Major: 2, Minor: 45, Patch: 1},
		},
		{
			name:   "version with leading/trailing whitespace",
			output: "  git version 2.46.0  ",
			want:   Version{Major: 2, Minor: 46, Patch: 0},
		},
		{
			name:    "invalid format",
			output:  "2.45.1",
			wantErr: true,
		},
		{
			name:    "empty output",
			output:  "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseVersion(tt.output)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseVersion() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Errorf("parseVersion() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestVersionAtLeast tests version comparison.
func TestVersionAtLeast(t *testing.T) {
	tests := []struct {
		name string
		v    Version
		min  Version
		want bool
	}{
		{
			name: "equal versions",
			v:    Version{2, 45, 0},
			min:  Version{2, 45, 0},
			want: true,
		},
		{
			name: "newer patch",
			v:    Version{2, 45, 1},
			min:  Version{2, 45, 0},
			want: true,
		},
		{
			name: "newer minor",
			v:    Version{2, 46, 0},
			min:  Version{2, 45, 0},
			want: true,
		},
		{
			name: "newer major",
			v:    Version{3, 0, 0},
			min:  Version{2, 45, 0},
			want: true,
		},
		{
			name: "older patch",
			v:    Version{2, 44, 9},
			min:  Version{2, 45, 0},
			want: false,
		},
		{
			name: "older minor",
			v:    Version{2, 44, 0},
			min:  Version{2, 45, 0},
			want: false,
		},
		{
			name: "older major",
			v:    Version{1, 9, 0},
			min:  Version{2, 45, 0},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.v.AtLeast(tt.min)
			if got != tt.want {
				t.Errorf("AtLeast(%v) = %v, want %v", tt.min, got, tt.want)
			}
		})
	}
}
