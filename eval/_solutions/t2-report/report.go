package task

import (
	"fmt"
	"sort"
	"strings"
)

// Report renders one "<name>: <average>" line per row, ordered by name, with the
// average written to one decimal place. An empty list renders an empty string.
func Report(rows []Row) string {
	sorted := append([]Row(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	var out strings.Builder
	for _, row := range sorted {
		fmt.Fprintf(&out, "%s: %.1f\n", row.Name, row.Average())
	}
	return out.String()
}
