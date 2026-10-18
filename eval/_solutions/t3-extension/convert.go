package task

// ToCelsius converts a Fahrenheit temperature to Celsius, truncated towards zero.
func ToCelsius(f float64) int {
	return int((f - 32) * 5 / 9)
}

// ToFahrenheit converts a Celsius temperature to Fahrenheit.
func ToFahrenheit(c int) float64 {
	return float64(c)*9/5 + 32
}
