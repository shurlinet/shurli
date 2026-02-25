package main

import (
	"reflect"
	"testing"
)

func TestReorderArgs(t *testing.T) {
	boolFlags := map[string]bool{"json": true}

	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "flags already first",
			args: []string{"--json", "-c", "3", "laptop"},
			want: []string{"--json", "-c", "3", "laptop"},
		},
		{
			name: "target before flags",
			args: []string{"laptop", "--json"},
			want: []string{"--json", "laptop"},
		},
		{
			name: "target between flags",
			args: []string{"laptop", "--json", "-c", "3"},
			want: []string{"--json", "-c", "3", "laptop"},
		},
		{
			name: "target first with mixed flags",
			args: []string{"laptop", "-c", "5", "--json", "--interval", "2s"},
			want: []string{"-c", "5", "--json", "--interval", "2s", "laptop"},
		},
		{
			name: "only target",
			args: []string{"laptop"},
			want: []string{"laptop"},
		},
		{
			name: "only flags",
			args: []string{"--json", "-c", "3"},
			want: []string{"--json", "-c", "3"},
		},
		{
			name: "flag with equals",
			args: []string{"laptop", "--config=/path/to/config"},
			want: []string{"--config=/path/to/config", "laptop"},
		},
		{
			name: "empty args",
			args: []string{},
			want: nil, // append(nil, nil...) = nil
		},
		{
			name: "bool flag between value flags",
			args: []string{"-c", "10", "home-server", "--json", "--interval", "500ms"},
			want: []string{"-c", "10", "--json", "--interval", "500ms", "home-server"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reorderArgs(tt.args, boolFlags)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("reorderArgs(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}
