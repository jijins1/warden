package git

import (
	"testing"
)

// TestNewSHA tests SHA validation.
func TestNewSHA(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:    "valid SHA-1 (40 chars)",
			input:   "0123456789abcdef0123456789abcdef01234567",
			wantErr: false,
		},
		{
			name:    "valid SHA-256 (64 chars)",
			input:   "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			wantErr: false,
		},
		{
			name:    "uppercase not allowed",
			input:   "0123456789ABCDEF0123456789ABCDEF01234567",
			wantErr: true,
		},
		{
			name:    "too short",
			input:   "0123456789abcdef0123456789abcdef",
			wantErr: true,
		},
		{
			name:    "too long (41 chars)",
			input:   "0123456789abcdef0123456789abcdef012345670",
			wantErr: true,
		},
		{
			name:    "contains invalid chars",
			input:   "0123456789abcdef0123456789abcdefGG234567",
			wantErr: true,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewSHA(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewSHA() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestNewRef tests Ref validation.
func TestNewRef(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:    "valid branch ref",
			input:   "refs/heads/main",
			wantErr: false,
		},
		{
			name:    "valid tag ref",
			input:   "refs/tags/v1.0.0",
			wantErr: false,
		},
		{
			name:    "valid audit ref",
			input:   "refs/audit/123/01JZQ00000000000000000ABCD",
			wantErr: false,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
		{
			name:    "too short",
			input:   "ref",
			wantErr: true,
		},
		{
			name:    "doesn't start with refs/",
			input:   "heads/main",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewRef(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewRef() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestNewNamespace tests Namespace creation.
func TestNewNamespace(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:    "valid namespace",
			input:   "runs/01JZQ00000000000000000ABCD",
			wantErr: false,
		},
		{
			name:    "simple namespace",
			input:   "runs",
			wantErr: false,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},

		// What follows is not tidiness. A Namespace composes
		// refs/namespaces/<ns>/, and ADR-0007 rests the whole isolation of a run
		// on that path being exactly the one the server intended. Each case below
		// is a way of stepping outside it.
		{
			name:    "parent traversal escapes into another run's namespace",
			input:   "runs/../runs/01JZQ00000000000000000ABCD",
			wantErr: true,
		},
		{
			name:    "bare parent component",
			input:   "..",
			wantErr: true,
		},
		{
			name:    "leading slash makes the composed path absolute",
			input:   "/runs/01JZQ00000000000000000ABCD",
			wantErr: true,
		},
		{
			name:    "trailing slash yields an empty component",
			input:   "runs/",
			wantErr: true,
		},
		{
			name:    "empty inner component",
			input:   "runs//01JZQ00000000000000000ABCD",
			wantErr: true,
		},
		{
			name:    "newline splits the line-oriented output of every ref command",
			input:   "runs/a\nruns/b",
			wantErr: true,
		},
		{
			name:    "space is refused by check-ref-format",
			input:   "runs/a b",
			wantErr: true,
		},
		{
			name:    "revision syntax would be read as an expression",
			input:   "runs/a^b",
			wantErr: true,
		},
		{
			name:    "component starting with a dot",
			input:   "runs/.git",
			wantErr: true,
		},
		{
			name:    "lock suffix collides with git's own lock files",
			input:   "runs/a.lock",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewNamespace(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewNamespace() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestSHAValid tests SHA.Valid() method.
func TestSHAValid(t *testing.T) {
	tests := []struct {
		sha   SHA
		valid bool
	}{
		{
			sha:   SHA("0123456789abcdef0123456789abcdef01234567"),
			valid: true,
		},
		{
			sha:   SHA("invalid"),
			valid: false,
		},
		{
			sha:   SHA(""),
			valid: false,
		},
	}

	for _, tt := range tests {
		got := tt.sha.Valid()
		if got != tt.valid {
			t.Errorf("SHA(%q).Valid() = %v, want %v", tt.sha, got, tt.valid)
		}
	}
}
