package main

import (
	"strings"
	"testing"
)

// UT-11 — TestSlugValidation_AcceptsAndRejects
//
// Table-driven test for the S3 plan-id slug regex (cli/s3upload.go:15):
//
//	^[a-z0-9][a-z0-9-]{0,62}$
//
// Note: there is no standalone slug-validation function in production —
// validation is performed inline in updateS3Slug at cli/s3upload.go:27–31
// via slugRe.MatchString. This test exercises the regex directly, which is
// what the inline validator depends on.
func TestSlugValidation_AcceptsAndRejects(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"simple_lowercase", "abc", true},
		{"with_digits_and_hyphen", "abc-123", true},
		{"single_char", "a", true},
		{"starts_with_digit", "0abc", true},
		{"max_length_63", "a" + strings.Repeat("b", 62), true},
		{"trailing_hyphen", "abc-", true},
		{"uppercase_rejected", "ABC", false},
		{"leading_hyphen_rejected", "-abc", false},
		{"embedded_space_rejected", "abc def", false},
		{"over_length_64", "a" + strings.Repeat("b", 63), false},
		{"empty_string_rejected", "", false},
		{"underscore_rejected", "abc_def", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := slugRe.MatchString(tc.input)
			if got != tc.want {
				t.Errorf("slugRe.MatchString(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}
