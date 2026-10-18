package task

import (
	"fmt"
	"strings"
)

// Report renders one "<name>: <average>" line per row, ordered by name, with the
// average written to one decimal place. An empty list renders an empty string.
func Report(rows []Row) string {
	var out strings.Builder
	for _, row := range rows {
		fmt.Fprintf(&out, "%s: %.0f\n", row.Name, row.Average())
	}
	return out.String()
}
