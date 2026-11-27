package main

import (
	"os"
	"regexp"
	"testing"
)

// The interface is one file with one script. Two elements with the same id, or a script that
// asks for an id that is not there, break buttons without any error: version 0.4.0 shipped with
// a dialog that had taken the ids of another.
func TestInterfaceIDs(t *testing.T) {
	raw, err := os.ReadFile("ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, m := range regexp.MustCompile(`\bid="([^"]+)"`).FindAllSubmatch(raw, -1) {
		if seen[string(m[1])] {
			t.Errorf("two elements have the id %q", m[1])
		}
		seen[string(m[1])] = true
	}
	for _, m := range regexp.MustCompile(`\$\("([^"]+)"\)`).FindAllSubmatch(raw, -1) {
		if !seen[string(m[1])] {
			t.Errorf("the script uses the id %q, which no element has", m[1])
		}
	}
}
