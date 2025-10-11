package api

import (
	"fmt"
	"testing"
	"time"
)

// TestLinkValid tests the Link.Valid method for various scenarios
func TestLinkValid(t *testing.T) {
	tests := []struct {
		name     string
		link     *Link
		expected bool
		desc     string
	}{
		{
			name:     "nil link",
			link:     nil,
			expected: false,
			desc:     "nil link should be invalid",
		},
		{
			name:     "empty URL",
			link:     &Link{URL: ""},
			expected: false,
			desc:     "empty URL should be invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.link.Valid()
			if result != tt.expected {
				t.Errorf("Link.Valid() = %v, expected %v. %s", result, tt.expected, tt.desc)
			}
		})
	}
}
