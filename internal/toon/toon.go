// Package toon encodes Moneta's agent-facing output as TOON
// (Token-Oriented Object Notation), per docs/decisions/0004.
//
// The encoder is encode-only and covers the spec subset Moneta emits:
// scalar fields, nested objects, and uniform tabular arrays, encoded with
// the comma document delimiter and two-space indentation. Decimals are
// caller-formatted through Number so money never passes through a float.
// Encoding follows toon-format/spec v3.3: quoting per section 7.2, escaping
// per section 7.1, canonical integers per section 2, no trailing spaces,
// and no trailing newline.
package toon

import (
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// Object is an ordered list of key-value fields. Key order is preserved
// exactly as encountered, as TOON requires.
type Object []Field

// Field is one key-value entry in an Object. Value must be a scalar (nil,
// bool, string, int, int64, Number), a nested Object, or a Table.
type Field struct {
	Key   string
	Value any
}

// Table is a uniform array of objects encoded in TOON tabular form:
// key[N]{field1,field2}: followed by one delimiter-joined row per record.
// Every row must carry exactly len(Fields) scalar cells.
type Table struct {
	Fields []string
	Rows   [][]any
}

// Number is a pre-formatted canonical decimal (section 2: no leading zeros
// beyond "0", no fractional trailing zeros, no exponent, no negative zero).
// Callers format integer cents to dollars at the output boundary and pass
// the string here so money never becomes a float. Invalid forms fail
// encoding instead of emitting non-canonical output.
type Number string

var (
	canonicalNumberPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]*[1-9])?$`)
	numericLikePattern     = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)
	unquotedKeyPattern     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)
)

// ValidNumber reports whether number is in canonical decimal form (spec
// section 2): no leading zeros beyond "0", no fractional trailing zeros,
// no exponent, and no negative zero.
func ValidNumber(number Number) bool {
	return canonicalNumberPattern.MatchString(string(number)) && string(number) != "-0"
}

// Marshal encodes doc and returns the document without a trailing newline.
func Marshal(doc Object) (string, error) {
	var builder strings.Builder
	if err := encodeObject(&builder, doc, 0); err != nil {
		return "", err
	}
	return builder.String(), nil
}

// Encode writes doc to w followed by a single trailing newline, which is
// the stdout boundary form for CLI output.
func Encode(w io.Writer, doc Object) error {
	text, err := Marshal(doc)
	if err != nil {
		return err
	}
	if text == "" {
		return nil
	}
	_, err = io.WriteString(w, text+"\n")
	return err
}

func encodeObject(builder *strings.Builder, object Object, depth int) error {
	seen := make(map[string]bool, len(object))
	for i, field := range object {
		if seen[field.Key] {
			return fmt.Errorf("duplicate object key %q", field.Key)
		}
		seen[field.Key] = true
		if i > 0 {
			builder.WriteString("\n")
		}
		if err := encodeField(builder, field, depth); err != nil {
			return err
		}
	}
	return nil
}

func encodeField(builder *strings.Builder, field Field, depth int) error {
	indent := strings.Repeat("  ", depth)
	key := encodeKey(field.Key)

	switch value := field.Value.(type) {
	case Object:
		builder.WriteString(indent + key + ":")
		if len(value) > 0 {
			builder.WriteString("\n")
			if err := encodeObject(builder, value, depth+1); err != nil {
				return err
			}
		}
	case Table:
		return encodeTable(builder, indent, key, value, depth)
	default:
		scalar, err := encodeScalar(value)
		if err != nil {
			return fmt.Errorf("field %q: %w", field.Key, err)
		}
		builder.WriteString(indent + key + ": " + scalar)
	}
	return nil
}

func encodeTable(builder *strings.Builder, indent, key string, table Table, depth int) error {
	fields := make([]string, len(table.Fields))
	for i, name := range table.Fields {
		fields[i] = encodeKey(name)
	}
	fmt.Fprintf(builder, "%s%s[%d]{%s}:",
		indent,
		key,
		len(table.Rows),
		strings.Join(fields, ","),
	)
	rowIndent := strings.Repeat("  ", depth+1)
	for rowIndex, row := range table.Rows {
		if len(row) != len(table.Fields) {
			return fmt.Errorf(
				"row %d has %d cells, want %d",
				rowIndex,
				len(row),
				len(table.Fields),
			)
		}
		cells := make([]string, len(row))
		for i, cell := range row {
			switch cell.(type) {
			case Object, Table:
				return fmt.Errorf("row %d cell %d: tabular cells must be scalars", rowIndex, i)
			}
			encoded, err := encodeScalar(cell)
			if err != nil {
				return fmt.Errorf("row %d cell %d: %w", rowIndex, i, err)
			}
			cells[i] = encoded
		}
		builder.WriteString("\n" + rowIndent + strings.Join(cells, ","))
	}
	return nil
}

// encodeScalar renders one primitive. Strings are quoted per spec section
// 7.2 with the comma delimiter in scope.
func encodeScalar(value any) (string, error) {
	switch typed := value.(type) {
	case nil:
		return "null", nil
	case bool:
		return strconv.FormatBool(typed), nil
	case string:
		return encodeString(typed), nil
	case int:
		return strconv.Itoa(typed), nil
	case int64:
		return strconv.FormatInt(typed, 10), nil
	case Number:
		if !ValidNumber(typed) {
			return "", fmt.Errorf("number %q is not in canonical decimal form", string(typed))
		}
		return string(typed), nil
	default:
		return "", fmt.Errorf("unsupported value type %T", value)
	}
}

// encodeString quotes per spec section 7.2 and escapes per section 7.1.
func encodeString(value string) string {
	if !needsQuotes(value) {
		return value
	}
	return quoteString(value)
}

func quoteString(value string) string {
	var builder strings.Builder
	builder.WriteByte('"')
	for _, r := range value {
		switch r {
		case '\\':
			builder.WriteString(`\\`)
		case '"':
			builder.WriteString(`\"`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&builder, `\u%04x`, r)
			} else {
				builder.WriteRune(r)
			}
		}
	}
	builder.WriteByte('"')
	return builder.String()
}

// needsQuotes reports the section 7.2 quoting conditions for the comma
// document delimiter.
func needsQuotes(value string) bool {
	if value == "" {
		return true
	}
	if value != strings.TrimSpace(value) {
		return true
	}
	if value == "true" || value == "false" || value == "null" {
		return true
	}
	if numericLikePattern.MatchString(value) {
		return true
	}
	if strings.HasPrefix(value, "-") {
		return true
	}
	return strings.ContainsAny(value, ":\"\\[]{},\n\r\t") || hasControlRune(value)
}

func hasControlRune(value string) bool {
	for _, r := range value {
		if r < 0x20 {
			return true
		}
	}
	return false
}

// encodeKey leaves keys matching the section 7.3 unquoted pattern bare and
// quotes everything else, even when section 7.2 would not require quotes
// for a value in the same shape.
func encodeKey(key string) string {
	if unquotedKeyPattern.MatchString(key) {
		return key
	}
	return quoteString(key)
}
