package main

import "testing"

// TestParseArgs pins the one argument this binary takes. The space form
// (`--config a.yaml`) used to be ignored and the Collector read ./config.yaml
// instead, so an argument that was given but not understood must now be
// refused rather than silently replaced by the default.
func TestParseArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    string
		wantErr bool
	}{
		{name: "equals form", args: []string{"--config=a.yaml"}, want: "a.yaml"},
		{name: "space form", args: []string{"--config", "a.yaml"}, want: "a.yaml"},
		{name: "single dash", args: []string{"-config", "a.yaml"}, want: "a.yaml"},
		{name: "no arguments", args: nil, want: "config.yaml"},
		{name: "unknown flag", args: []string{"--bogus"}, wantErr: true},
		{name: "positional argument", args: []string{"a.yaml"}, wantErr: true},
		{name: "config without a value", args: []string{"--config"}, wantErr: true},
		{name: "config with an empty value", args: []string{"--config="}, wantErr: true},
		{name: "positional after config", args: []string{"--config=a.yaml", "b.yaml"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, err := parseArgs(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseArgs(%q) error = %v, want error %t", tt.args, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("parseArgs(%q) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}
