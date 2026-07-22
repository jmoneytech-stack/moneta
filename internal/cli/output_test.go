package cli

import (
	"bytes"
	"math"
	"strings"
	"testing"

	"github.com/jmoneytech-stack/moneta/internal/toon"
)

func testDoc() toon.Object {
	return toon.Object{
		{Key: "summary", Value: toon.Object{
			{Key: "items", Value: 1},
			{Key: "total", Value: toon.Number("-4821.37")},
		}},
		{Key: "items", Value: toon.Table{
			Fields: []string{"name", "count"},
			Rows:   [][]any{{"Widget", 3}},
		}},
		{Key: "hint", Value: "next step"},
	}
}

func TestRenderTOON(t *testing.T) {
	var buffer bytes.Buffer
	if err := Render(&buffer, testDoc(), FormatTOON); err != nil {
		t.Fatalf("Render() error: %v", err)
	}
	want := "summary:\n" +
		"  items: 1\n" +
		"  total: -4821.37\n" +
		"items[1]{name,count}:\n" +
		"  Widget,3\n" +
		"hint: next step\n"
	if buffer.String() != want {
		t.Errorf("Render(TOON) =\n%s\nwant:\n%s", buffer.String(), want)
	}
}

func TestRenderJSON(t *testing.T) {
	var buffer bytes.Buffer
	if err := Render(&buffer, testDoc(), FormatJSON); err != nil {
		t.Fatalf("Render() error: %v", err)
	}
	want := `{"summary":{"items":1,"total":-4821.37},` +
		`"items":[{"name":"Widget","count":3}],"hint":"next step"}` + "\n"
	if buffer.String() != want {
		t.Errorf("Render(JSON) = %q, want %q", buffer.String(), want)
	}
}

func TestRenderRejectsInvalidDocuments(t *testing.T) {
	tests := []struct {
		name   string
		doc    toon.Object
		format Format
	}{
		{
			name:   "toon unsupported type",
			doc:    toon.Object{{Key: "v", Value: 1.5}},
			format: FormatTOON,
		},
		{
			name:   "json unsupported type",
			doc:    toon.Object{{Key: "v", Value: 1.5}},
			format: FormatJSON,
		},
		{
			name:   "json non-canonical number",
			doc:    toon.Object{{Key: "v", Value: toon.Number("1.50")}},
			format: FormatJSON,
		},
		{
			name:   "unknown format",
			doc:    testDoc(),
			format: Format(42),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var buffer bytes.Buffer
			if err := Render(&buffer, test.doc, test.format); err == nil {
				t.Errorf("Render() = %q, want an error", buffer.String())
			}
		})
	}
}

func TestMoneyFormatsCanonicalDecimals(t *testing.T) {
	tests := []struct {
		cents int64
		want  toon.Number
	}{
		{0, "0"},
		{1, "0.01"},
		{-1, "-0.01"},
		{100, "1"},
		{-500, "-5"},
		{1050, "10.5"},
		{-4218, "-42.18"},
		{999999999, "9999999.99"},
		// The negative-magnitude overflow boundary: must still produce the
		// exact canonical decimal instead of wrapping.
		{math.MinInt64, "-92233720368547758.08"},
	}
	for _, test := range tests {
		got := Money(test.cents)
		if got != test.want {
			t.Errorf("Money(%d) = %q, want %q", test.cents, got, test.want)
		}
		if !toon.ValidNumber(got) {
			t.Errorf("Money(%d) = %q is not canonical", test.cents, got)
		}
	}
}

func TestRenderJSONFieldOrderPreserved(t *testing.T) {
	var buffer bytes.Buffer
	doc := toon.Object{
		{Key: "zebra", Value: 1},
		{Key: "apple", Value: 2},
	}
	if err := Render(&buffer, doc, FormatJSON); err != nil {
		t.Fatalf("Render() error: %v", err)
	}
	if !strings.HasPrefix(buffer.String(), `{"zebra":1,"apple":2}`) {
		t.Errorf("Render(JSON) reordered fields: %q", buffer.String())
	}
}
