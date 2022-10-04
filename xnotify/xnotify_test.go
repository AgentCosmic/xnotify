package xnotify

import "testing"

func TestFullURL(t *testing.T) {
	type fullURLTest struct {
		url      string
		expected string
	}
	testCases := []fullURLTest{
		{url: "example.com", expected: "http://example.com"},
		{url: "http://example.com", expected: "http://example.com"},
		{url: "://example.com", expected: "localhost://example.com"},
	}
	for _, testCase := range testCases {
		if res := fullURL(testCase.url); res != testCase.expected {
			t.Errorf("Expected %q, got %q", testCase.expected, res)
		}
	}
}
