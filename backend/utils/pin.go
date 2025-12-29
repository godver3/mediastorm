package utils

// ValidatePIN checks if a string is a valid 6-digit PIN.
// Used for profile PINs (parental controls).
func ValidatePIN(pin string) bool {
	if len(pin) != 6 {
		return false
	}

	// Check if all characters are digits
	for _, char := range pin {
		if char < '0' || char > '9' {
			return false
		}
	}

	return true
}
