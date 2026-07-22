package toon

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goldenCases encode documents and compare against testdata goldens derived
// from the official TOON spec examples (toon-format/spec v3.3).
func TestMarshalGoldenFiles(t *testing.T) {
	tests := []struct {
		name   string
		doc    Object
		golden string
	}{
		{
			name: "scalars",
			doc: Object{
				{Key: "name", Value: "Alice"},
				{Key: "age", Value: int64(30)},
				{Key: "active", Value: true},
				{Key: "score", Value: Number("9.5")},
				{Key: "nickname", Value: nil},
			},
			golden: "scalars.toon",
		},
		{
			name: "nested object",
			doc: Object{
				{Key: "user", Value: Object{
					{Key: "id", Value: int64(123)},
					{Key: "name", Value: "Ada"},
					{Key: "address", Value: Object{
						{Key: "city", Value: "Portland"},
						{Key: "zip", Value: "97205"},
					}},
				}},
				{Key: "empty", Value: Object{}},
			},
			golden: "nested_object.toon",
		},
		{
			name: "tabular",
			doc: Object{
				{Key: "summary", Value: Object{
					{Key: "count", Value: 2},
				}},
				{Key: "users", Value: Table{
					Fields: []string{"id", "name", "role"},
					Rows: [][]any{
						{int64(1), "Alice", "admin"},
						{int64(2), "Bob", "user"},
					},
				}},
			},
			golden: "tabular.toon",
		},
		{
			name: "quoting",
			doc: Object{
				{Key: "empty", Value: ""},
				{Key: "padded", Value: " padded "},
				{Key: "bool_like", Value: "true"},
				{Key: "null_like", Value: "null"},
				{Key: "numeric_like", Value: "05"},
				{Key: "negative_like", Value: "-5"},
				{Key: "with_colon", Value: "a: b"},
				{Key: "with_comma", Value: "hello, world"},
				{Key: "with_brackets", Value: "a[0]"},
				{Key: "with_quote", Value: `say "hi"`},
				{Key: "with_backslash", Value: `C:\temp`},
				{Key: "hyphen_only", Value: "-"},
				{Key: "unicode", Value: "cafe ☕"},
				{Key: "spaces_ok", Value: "internal spaces are fine"},
			},
			golden: "quoting.toon",
		},
		{
			name: "status shape",
			doc: Object{
				{Key: "summary", Value: Object{
					{Key: "items", Value: 2},
					{Key: "accounts", Value: 5},
					{Key: "needs_attention", Value: 1},
				}},
				{Key: "items", Value: Table{
					Fields: []string{
						"provider", "item", "institution", "status",
						"accounts", "transactions", "last_sync",
					},
					Rows: [][]any{
						{"plaid", "item-1", "Example Bank", "ok", 3, 142, "2026-07-20T14:03:11.123Z"},
						{"plaid", "item-2", "Other Union", "login_required", 2, 98, ""},
					},
				}},
				{Key: "hint", Value: "re-run moneta link to reconnect items with status login_required"},
			},
			golden: "status_shape.toon",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Marshal(test.doc)
			if err != nil {
				t.Fatalf("Marshal() error: %v", err)
			}
			want, err := os.ReadFile(filepath.Join("testdata", test.golden))
			if err != nil {
				t.Fatalf("read golden: %v", err)
			}
			if got != strings.TrimSuffix(string(want), "\n") {
				t.Errorf("Marshal() =\n%s\nwant golden:\n%s", got, want)
			}
		})
	}
}

func TestMarshalEdgeCases(t *testing.T) {
	tests := []struct {
		name string
		doc  Object
		want string
	}{
		{
			name: "empty document",
			doc:  Object{},
			want: "",
		},
		{
			name: "empty table keeps field header",
			doc: Object{
				{Key: "items", Value: Table{Fields: []string{"a", "b"}}},
			},
			want: "items[0]{a,b}:",
		},
		{
			name: "quoted key",
			doc:  Object{{Key: "my-key", Value: 1}},
			want: `"my-key": 1`,
		},
		{
			name: "string escapes in control range",
			doc:  Object{{Key: "v", Value: "line\nbreak\x01"}},
			want: `v: "line\nbreak\u0001"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Marshal(test.doc)
			if err != nil {
				t.Fatalf("Marshal() error: %v", err)
			}
			if got != test.want {
				t.Errorf("Marshal() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestMarshalRejectsInvalidDocuments(t *testing.T) {
	tests := []struct {
		name string
		doc  Object
	}{
		{
			name: "unsupported type",
			doc:  Object{{Key: "v", Value: 1.5}},
		},
		{
			name: "non-canonical number trailing zero",
			doc:  Object{{Key: "v", Value: Number("1.50")}},
		},
		{
			name: "non-canonical number leading zero",
			doc:  Object{{Key: "v", Value: Number("05")}},
		},
		{
			name: "non-canonical negative zero",
			doc:  Object{{Key: "v", Value: Number("-0")}},
		},
		{
			name: "duplicate key",
			doc: Object{
				{Key: "v", Value: 1},
				{Key: "v", Value: 2},
			},
		},
		{
			name: "ragged table row",
			doc: Object{{Key: "t", Value: Table{
				Fields: []string{"a", "b"},
				Rows:   [][]any{{1}},
			}}},
		},
		{
			name: "nested object inside table cell",
			doc: Object{{Key: "t", Value: Table{
				Fields: []string{"a"},
				Rows:   [][]any{{Object{{Key: "x", Value: 1}}}},
			}}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got, err := Marshal(test.doc); err == nil {
				t.Errorf("Marshal() = %q, want an error", got)
			}
		})
	}
}

// TestNumberCannotInjectStructure pins the load-bearing safety property:
// the anchored canonical-number regex rejects any payload carrying TOON
// structure. This test must fail if the pattern is loosened (for example
// with (?m) or an unanchored rewrite).
func TestNumberCannotInjectStructure(t *testing.T) {
	injections := []Number{
		"5\ninjected: true",
		"5\n",
		"5 true",
		"5,admin",
		"5]\n",
		"0x05",
		"5e0", // exponent form is outside the canonical subset
		"+5",  // leading plus is not canonical
		" 5",  // whitespace is not canonical
		"5 ",
	}
	for _, injection := range injections {
		doc := Object{{Key: "n", Value: injection}}
		if got, err := Marshal(doc); err == nil {
			t.Errorf("Marshal(Number(%q)) = %q, want an error", string(injection), got)
		}
	}

	// A plain canonical number still round-trips unquoted.
	got, err := Marshal(Object{{Key: "n", Value: Number("5")}})
	if err != nil {
		t.Fatalf("Marshal(Number(5)) error: %v", err)
	}
	if got != "n: 5" {
		t.Errorf("Marshal(Number(5)) = %q, want %q", got, "n: 5")
	}
}

// TestTableRejectsDuplicateColumns mirrors the object duplicate-key guard:
// a tabular header with repeated field names is an encoder error.
func TestTableRejectsDuplicateColumns(t *testing.T) {
	doc := Object{{Key: "t", Value: Table{
		Fields: []string{"a", "a"},
		Rows:   [][]any{{1, 2}},
	}}}
	if got, err := Marshal(doc); err == nil {
		t.Errorf("Marshal() = %q, want a duplicate column error", got)
	}
}

// TestLineSeparatorsAreQuoted pins the defensive tightening for values that
// flow from untrusted provider data: JS line separators and friends are
// never emitted raw, even though the spec does not require quoting them.
func TestLineSeparatorsAreQuoted(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{"line separator", "a\u2028b"},
		{"paragraph separator", "a\u2029b"},
		{"next line", "a\u0085b"},
		{"delete", "a\u007fb"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Marshal(Object{{Key: "v", Value: test.value}})
			if err != nil {
				t.Fatalf("Marshal() error: %v", err)
			}
			want := "v: \"" + test.value + "\""
			if got != want {
				t.Errorf("Marshal() = %q, want quoted %q", got, want)
			}
		})
	}
}

func TestEncodeAppendsSingleTrailingNewline(t *testing.T) {
	var buffer bytes.Buffer
	doc := Object{{Key: "a", Value: 1}}
	if err := Encode(&buffer, doc); err != nil {
		t.Fatalf("Encode() error: %v", err)
	}
	if buffer.String() != "a: 1\n" {
		t.Errorf("Encode() = %q, want %q", buffer.String(), "a: 1\n")
	}

	buffer.Reset()
	if err := Encode(&buffer, Object{}); err != nil {
		t.Fatalf("Encode() empty error: %v", err)
	}
	if buffer.String() != "" {
		t.Errorf("Encode() empty = %q, want no output", buffer.String())
	}
}
