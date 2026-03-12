package main

import (
	"flag"
	"strings"
	"testing"
)

func TestReorderFlags(t *testing.T) {
	tests := []struct {
		name string
		args string
		want string
	}{
		{
			name: "flags already first",
			args: "--follow --streams 4 file.txt peer",
			want: "--follow --streams 4 file.txt peer",
		},
		{
			name: "flags after positional",
			args: "file.txt peer --follow",
			want: "--follow file.txt peer",
		},
		{
			name: "mixed flags and positional",
			args: "file.txt --follow peer --streams 4",
			want: "--follow --streams 4 file.txt peer",
		},
		{
			name: "flag with equals",
			args: "file.txt --streams=4 peer --follow",
			want: "--streams=4 --follow file.txt peer",
		},
		{
			name: "no flags",
			args: "file.txt peer",
			want: "file.txt peer",
		},
		{
			name: "only flags",
			args: "--follow --json",
			want: "--follow --json",
		},
		{
			name: "double dash stops processing",
			args: "--follow -- --not-a-flag file.txt",
			want: "--follow -- --not-a-flag file.txt",
		},
		{
			name: "empty args",
			args: "",
			want: "",
		},
		{
			name: "single dash flag",
			args: "file.txt -c 5 peer",
			want: "-c 5 file.txt peer",
		},
		{
			name: "bool flag no value consumed",
			args: "file.txt --follow peer",
			want: "--follow file.txt peer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			fs.Bool("follow", false, "")
			fs.Bool("json", false, "")
			fs.Int("streams", 0, "")
			fs.Int("c", 0, "")

			var args []string
			if tt.args != "" {
				args = strings.Fields(tt.args)
			}

			got := reorderFlags(fs, args)
			gotStr := strings.Join(got, " ")

			if gotStr != tt.want {
				t.Errorf("reorderFlags(%q) = %q, want %q", tt.args, gotStr, tt.want)
			}
		})
	}
}
