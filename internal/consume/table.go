// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package consume

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
)

// columns are the header of the CSV and Markdown exports, in the order of
// Row's exported fields: the three formats carry the same rows.
var columns = []string{"recipe", "recipeVersion", "ingredient", "kind", "source", "destination", "digest"}

func (r *Row) cells() []string {
	return []string{r.Recipe, r.RecipeVersion, r.Ingredient, r.Kind, r.Source, r.Destination, r.Digest}
}

// Render writes the table in one of the three export formats. The output
// is deterministic: same rows, same bytes, no timestamp.
func (t *Table) Render(f Format) ([]byte, error) {
	switch f {
	case FormatCSV:
		var buf bytes.Buffer
		w := csv.NewWriter(&buf)
		if err := w.Write(columns); err != nil {
			return nil, err
		}
		for i := range t.Entries {
			if err := w.Write(t.Entries[i].cells()); err != nil {
				return nil, err
			}
		}
		w.Flush()
		return buf.Bytes(), w.Error()
	case FormatMarkdown:
		var b strings.Builder
		b.WriteString("| " + strings.Join(columns, " | ") + " |\n")
		b.WriteString("|" + strings.Repeat(" --- |", len(columns)) + "\n")
		for i := range t.Entries {
			cells := t.Entries[i].cells()
			for j, c := range cells {
				// A pipe ends a cell, a newline ends a row: neither may
				// come out of a value.
				c = strings.ReplaceAll(c, "|", `\|`)
				cells[j] = strings.ReplaceAll(c, "\n", " ")
			}
			b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
		}
		return []byte(b.String()), nil
	default:
		out, err := json.MarshalIndent(t, "", "  ")
		if err != nil {
			return nil, err
		}
		return append(out, '\n'), nil
	}
}
