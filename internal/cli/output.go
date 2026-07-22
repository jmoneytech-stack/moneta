// Package cli holds the shared output path for Moneta's AXI read commands.
// Commands build a toon.Object document and render it here, so every read
// emits the same TOON shape on stdout with the same --json escape hatch.
package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"

	"github.com/jmoneytech-stack/moneta/internal/toon"
)

// Format selects the stdout encoding for a read command.
type Format int

const (
	// FormatTOON emits spec-subset TOON, the default agent-facing shape.
	FormatTOON Format = iota
	// FormatJSON emits compact single-line JSON, the --json escape hatch.
	FormatJSON
)

// Money converts signed integer cents to a canonical dollars decimal for
// the output boundary (section 2 form: no trailing zeros beyond the exact
// cents, no negative zero). Money never passes through a float.
func Money(cents int64) toon.Number {
	if cents == 0 {
		return "0"
	}
	// -cents overflows at the int64 boundary; render it explicitly so the
	// output stays canonical (mirrors the ingest-side MinInt64 guard).
	if cents == math.MinInt64 {
		return "-92233720368547758.08"
	}
	sign := ""
	magnitude := cents
	if cents < 0 {
		sign = "-"
		magnitude = -cents
	}
	dollars := magnitude / 100
	frac := magnitude % 100
	switch {
	case frac == 0:
		return toon.Number(fmt.Sprintf("%s%d", sign, dollars))
	case frac%10 == 0:
		return toon.Number(fmt.Sprintf("%s%d.%d", sign, dollars, frac/10))
	default:
		return toon.Number(fmt.Sprintf("%s%d.%02d", sign, dollars, frac))
	}
}

// Render writes doc to w in the requested format with a trailing newline.
// Internal logic stays on Go structs; conversion happens only here, at the
// stdout boundary.
func Render(w io.Writer, doc toon.Object, format Format) error {
	switch format {
	case FormatTOON:
		return toon.Encode(w, doc)
	case FormatJSON:
		var buffer bytes.Buffer
		if err := appendJSONValue(&buffer, doc); err != nil {
			return err
		}
		buffer.WriteByte('\n')
		_, err := w.Write(buffer.Bytes())
		return err
	default:
		return fmt.Errorf("unknown output format %d", int(format))
	}
}

// appendJSONValue encodes the toon document model as JSON, preserving field
// order (encoding/json would sort map keys).
func appendJSONValue(buffer *bytes.Buffer, value any) error {
	switch typed := value.(type) {
	case nil:
		buffer.WriteString("null")
	case bool:
		buffer.WriteString(strconv.FormatBool(typed))
	case string:
		writeJSONString(buffer, typed)
	case int:
		buffer.WriteString(strconv.Itoa(typed))
	case int64:
		buffer.WriteString(strconv.FormatInt(typed, 10))
	case toon.Number:
		if !toon.ValidNumber(typed) {
			return fmt.Errorf("number %q is not in canonical decimal form", string(typed))
		}
		buffer.WriteString(string(typed))
	case toon.Object:
		buffer.WriteByte('{')
		for i, field := range typed {
			if i > 0 {
				buffer.WriteByte(',')
			}
			writeJSONString(buffer, field.Key)
			buffer.WriteByte(':')
			if err := appendJSONValue(buffer, field.Value); err != nil {
				return err
			}
		}
		buffer.WriteByte('}')
	case toon.Table:
		buffer.WriteByte('[')
		for rowIndex, row := range typed.Rows {
			if len(row) != len(typed.Fields) {
				return fmt.Errorf(
					"row %d has %d cells, want %d",
					rowIndex,
					len(row),
					len(typed.Fields),
				)
			}
			if rowIndex > 0 {
				buffer.WriteByte(',')
			}
			buffer.WriteByte('{')
			for i, cell := range row {
				if i > 0 {
					buffer.WriteByte(',')
				}
				writeJSONString(buffer, typed.Fields[i])
				buffer.WriteByte(':')
				if err := appendJSONValue(buffer, cell); err != nil {
					return err
				}
			}
			buffer.WriteByte('}')
		}
		buffer.WriteByte(']')
	default:
		return fmt.Errorf("unsupported value type %T", value)
	}
	return nil
}

func writeJSONString(buffer *bytes.Buffer, value string) {
	encoded, _ := json.Marshal(value) // strings never fail
	buffer.Write(encoded)
}
