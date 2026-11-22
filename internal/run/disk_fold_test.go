package run

import "testing"

// THE FOLDED `disk:` MAPPING MUST MEAN EXACTLY WHAT THE FIVE KEYS MEANT.
//
// v0.0.1: `disk:` named a file and `disk_gb:`, `disk_gpt:`, `disk_esp_mib:`, `disk_min_mb:` and
// `disk_settle:` described it - six keys for one thing. The mapping is translated into those keys,
// so the code that creates and measures the disk is the one already measured.
func TestDiskMappingTranslatesToTheOldKeys(t *testing.T) {
	cases := []struct {
		name string
		spec map[string]any
		want map[string]any
	}{
		{
			name: "size",
			spec: map[string]any{"disk": map[string]any{"file": "d.qcow2", "size": 128}},
			want: map[string]any{"disk": "d.qcow2", "disk_gb": 128},
		},
		{
			name: "gpt and esp",
			spec: map[string]any{"disk": map[string]any{"file": "d.qcow2", "size": 128, "gpt": true, "esp_mib": 512}},
			want: map[string]any{"disk": "d.qcow2", "disk_gb": 128, "disk_gpt": true, "disk_esp_mib": 512},
		},
		{
			name: "settle and min",
			spec: map[string]any{"disk": map[string]any{"file": "d.qcow2", "min_mb": 4000, "wait": 300}},
			want: map[string]any{"disk": "d.qcow2", "disk_min_mb": 4000, "disk_settle": 300},
		},
	}
	for _, c := range cases {
		got := translateDisk(c.spec)
		if got == nil {
			t.Fatalf("%s: no translation", c.name)
		}
		for k, want := range c.want {
			if got[k] != want {
				t.Fatalf("%s: %q = %v, want %v (all: %v)", c.name, k, got[k], want, got)
			}
		}
		if _, stillMap := got["disk"].(map[string]any); stillMap {
			t.Fatalf("%s: `disk:` is still a mapping, so the reader would recurse", c.name)
		}
		if _, hasOld := got["size"]; hasOld {
			t.Fatalf("%s: a field name survived, and no code reads `size:`", c.name)
		}
	}
}

// A `disk:` that names a file with no mapping is left alone, so the 30 existing uses do not move.
func TestDiskStringIsUntouched(t *testing.T) {
	if got := translateDisk(map[string]any{"disk": "d.qcow2"}); got != nil {
		t.Fatalf("a string `disk:` must not translate, got %v", got)
	}
}
