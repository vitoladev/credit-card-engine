package domain_test

import (
	"testing"

	"engine/internal/domain"
)

func TestMaskCPF(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"39053344705", "***05"},
		{"12", "***"},
		{"", "***"},
		{"abc", "***bc"},
	}
	for _, tc := range cases {
		if got := domain.MaskCPF(tc.in); got != tc.want {
			t.Errorf("MaskCPF(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}
