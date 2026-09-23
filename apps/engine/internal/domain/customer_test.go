package domain_test

import (
	"testing"

	"engine/internal/domain"
)

func TestMaskCPF(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"39053344705", "390.***.***-05"},
		{"12345678909", "123.***.***-09"},
		{"12", "***"},
		{"", "***"},
		{"abc", "***"},
	}
	for _, tc := range cases {
		if got := domain.MaskCPF(tc.in); got != tc.want {
			t.Errorf("MaskCPF(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}
