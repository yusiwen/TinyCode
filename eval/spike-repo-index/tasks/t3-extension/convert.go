package task

// ToCelsius converts a Fahrenheit temperature to Celsius, truncated towards zero.
func ToCelsius(f float64) int {
	return int((f - 32) * 5 / 9)
}
