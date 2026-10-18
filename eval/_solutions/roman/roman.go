package task

// romanTable is the numeral set in descending order, including the subtractive
// pairs, so the rendering is a greedy walk.
var romanTable = []struct {
	value  int
	digits string
}{
	{1000, "M"}, {900, "CM"}, {500, "D"}, {400, "CD"},
	{100, "C"}, {90, "XC"}, {50, "L"}, {40, "XL"},
	{10, "X"}, {9, "IX"}, {5, "V"}, {4, "IV"}, {1, "I"},
}

// ToRoman renders n as a Roman numeral. It is only valid for 1 <= n <= 3999.
func ToRoman(n int) string {
	out := make([]byte, 0, 16)
	for _, entry := range romanTable {
		for n >= entry.value {
			out = append(out, entry.digits...)
			n -= entry.value
		}
	}
	return string(out)
}
