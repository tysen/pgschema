package diff

import "testing"

func TestExtractMigrateUsing(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantUsing    string
		wantClean    string
		wantOK       bool
	}{
		{
			name:      "empty",
			input:     "",
			wantUsing: "",
			wantClean: "",
			wantOK:    false,
		},
		{
			name:      "no directive",
			input:     "this is a regular comment",
			wantUsing: "",
			wantClean: "this is a regular comment",
			wantOK:    false,
		},
		{
			name:      "directive only",
			input:     "[pgschema migrate-using: ROW((c).a, (c).b)::new_t]",
			wantUsing: "ROW((c).a, (c).b)::new_t",
			wantClean: "",
			wantOK:    true,
		},
		{
			name:      "directive followed by user comment",
			input:     "[pgschema migrate-using: c::new_t] team season detail",
			wantUsing: "c::new_t",
			wantClean: "team season detail",
			wantOK:    true,
		},
		{
			name:      "directive with leading whitespace",
			input:     "  [pgschema migrate-using: x]  trailing",
			wantUsing: "x",
			wantClean: "trailing",
			wantOK:    true,
		},
		{
			name:      "user comment first does not match",
			input:     "team detail [pgschema migrate-using: x]",
			wantUsing: "",
			wantClean: "team detail [pgschema migrate-using: x]",
			wantOK:    false,
		},
		{
			name:      "wrong directive name",
			input:     "[pgschema something-else: x]",
			wantUsing: "",
			wantClean: "[pgschema something-else: x]",
			wantOK:    false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			using, clean, ok := extractMigrateUsing(tc.input)
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v", ok, tc.wantOK)
			}
			if using != tc.wantUsing {
				t.Errorf("using = %q, want %q", using, tc.wantUsing)
			}
			if clean != tc.wantClean {
				t.Errorf("clean = %q, want %q", clean, tc.wantClean)
			}
		})
	}
}

func TestStripMigrateUsingMatchesExtract(t *testing.T) {
	in := "[pgschema migrate-using: ROW(a,b)::t]  rest"
	if got, want := stripMigrateUsing(in), "rest"; got != want {
		t.Errorf("stripMigrateUsing(%q) = %q, want %q", in, got, want)
	}
}
