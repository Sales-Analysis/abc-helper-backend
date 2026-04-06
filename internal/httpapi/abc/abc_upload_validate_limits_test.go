package abc

import "testing"

func TestParseMemoryLimitBytes(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int64
		ok   bool
	}{
		{name: "mib", raw: "512MiB", want: 512 << 20, ok: true},
		{name: "gib", raw: "2GiB", want: 2 << 30, ok: true},
		{name: "mb", raw: "200MB", want: 200 * 1000 * 1000, ok: true},
		{name: "bytes", raw: "1048576", want: 1048576, ok: true},
		{name: "spaces", raw: " 1024MiB ", want: 1024 << 20, ok: true},
		{name: "invalid-unit", raw: "100foo", want: 0, ok: false},
		{name: "invalid-format", raw: "1.5GiB", want: 0, ok: false},
		{name: "empty", raw: "", want: 0, ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseMemoryLimitBytes(tt.raw)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("got = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestClampWorksheetXMLByGoMemoryLimit(t *testing.T) {
	t.Run("clamps by go limit", func(t *testing.T) {
		t.Setenv("GOMEMLIMIT", "512MiB")
		current := int64(1024) << 20
		got := clampWorksheetXMLByGoMemoryLimit(current)
		want := int64(128) << 20
		if got != want {
			t.Fatalf("got %d, want %d", got, want)
		}
	})

	t.Run("respects minimum", func(t *testing.T) {
		t.Setenv("GOMEMLIMIT", "100MiB")
		current := int64(1024) << 20
		got := clampWorksheetXMLByGoMemoryLimit(current)
		want := int64(minWorksheetXMLMB) << 20
		if got != want {
			t.Fatalf("got %d, want %d", got, want)
		}
	})

	t.Run("keeps current when lower", func(t *testing.T) {
		t.Setenv("GOMEMLIMIT", "4GiB")
		current := int64(256) << 20
		got := clampWorksheetXMLByGoMemoryLimit(current)
		if got != current {
			t.Fatalf("got %d, want %d", got, current)
		}
	})

	t.Run("invalid env keeps current", func(t *testing.T) {
		t.Setenv("GOMEMLIMIT", "oops")
		current := int64(700) << 20
		got := clampWorksheetXMLByGoMemoryLimit(current)
		if got != current {
			t.Fatalf("got %d, want %d", got, current)
		}
	})
}
