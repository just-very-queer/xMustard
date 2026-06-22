package workspaceops

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsInvalidInputClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"wrapped ErrInvalidInput", fmt.Errorf("content is required: %w", ErrInvalidInput), true},
		{"path-escape sentinel by identity", errEscape, true},
		{"wrapped safepath sentinel", fmt.Errorf("ctx: %w", errAbsPath), true},
		{"validateSafeID rejection", validateSafeID("workspace", "../escape"), true},
		{"empty-path sentinel", errEmptyPath, true},
		{"generic error stays 500-class", errors.New("rust-core exploded"), false},
		{"os-not-exist is its own 404 class", errors.New("file does not exist"), false},
	}
	for _, c := range cases {
		if got := IsInvalidInput(c.err); got != c.want {
			t.Errorf("%s: IsInvalidInput(%v)=%v want %v", c.name, c.err, got, c.want)
		}
	}
}
