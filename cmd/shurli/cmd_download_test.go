package main

import "testing"

func TestParsePeerPath(t *testing.T) {
	tests := []struct {
		input    string
		wantPeer string
		wantPath string
	}{
		{"home-server:/home/user/file.txt", "home-server", "/home/user/file.txt"},
		{"12D3KooWABC:/shared/photo.jpg", "12D3KooWABC", "/shared/photo.jpg"},
		{"my-peer:relative/path.txt", "my-peer", "relative/path.txt"},
		// Edge cases.
		{"", "", ""},           // empty
		{"nocolon", "", ""},    // no colon
		{"a:", "", ""},         // single char before colon (too short)
		{"ab:", "ab", ""},      // minimum 2 chars before colon, empty path
		{":path", "", ""},      // no peer (colon at position 0)
		{"x:path", "", ""},     // single char before colon
	}

	for _, tt := range tests {
		peer, path := parsePeerPath(tt.input)
		if peer != tt.wantPeer || path != tt.wantPath {
			t.Errorf("parsePeerPath(%q) = (%q, %q), want (%q, %q)",
				tt.input, peer, path, tt.wantPeer, tt.wantPath)
		}
	}
}
