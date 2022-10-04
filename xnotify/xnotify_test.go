package xnotify

import (
	"testing"

	"github.com/fsnotify/fsnotify"
)

func TestOpToString(t *testing.T) {
	type opToStringTest struct {
		opCode   fsnotify.Op
		expected string
	}
	testCases := []opToStringTest{
		{fsnotify.Create, "create"},
		{fsnotify.Write, "write"},
		{fsnotify.Remove, "remove"},
		{fsnotify.Rename, "rename"},
		{fsnotify.Chmod, "chmod"},
	}
	for _, testCase := range testCases {
		if res := opToString(testCase.opCode); res != testCase.expected {
			t.Errorf("Expected %q, got %q", testCase.expected, res)
		}
	}
}

func TestOpToStringInvalid(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("Expected panic for invalid fsnotify op code")
		}
	}()
	var invalidOp fsnotify.Op = 999
	opToString(invalidOp)
}

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
