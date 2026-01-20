package encryption

import (
	"testing"
)

func TestGenerateRandomNonce_Length(t *testing.T) {
	nonce, err := GenerateRandomNonce()
	if err != nil {
		t.Fatalf("GenerateRandomNonce failed: %v", err)
	}

	// Nonce should be exactly 24 bytes
	if len(nonce) != fileNonceSize {
		t.Errorf("expected nonce length %d, got %d", fileNonceSize, len(nonce))
	}
}

func TestGenerateRandomNonce_Uniqueness(t *testing.T) {
	// Generate multiple nonces and verify they are different
	const numNonces = 10
	nonces := make([]nonce, numNonces)

	for i := 0; i < numNonces; i++ {
		n, err := GenerateRandomNonce()
		if err != nil {
			t.Fatalf("GenerateRandomNonce iteration %d failed: %v", i, err)
		}
		nonces[i] = n
	}

	// Check that all nonces are unique
	for i := 0; i < numNonces; i++ {
		for j := i + 1; j < numNonces; j++ {
			if nonces[i] == nonces[j] {
				t.Errorf("nonces at index %d and %d are identical", i, j)
			}
		}
	}
}

func TestGenerateRandomNonce_NotAllZeros(t *testing.T) {
	nonce, err := GenerateRandomNonce()
	if err != nil {
		t.Fatalf("GenerateRandomNonce failed: %v", err)
	}

	allZeros := true
	for _, b := range nonce {
		if b != 0 {
			allZeros = false
			break
		}
	}

	if allZeros {
		t.Error("nonce should not be all zeros")
	}
}

func TestNonce_ToBytes(t *testing.T) {
	nonce, err := GenerateRandomNonce()
	if err != nil {
		t.Fatalf("GenerateRandomNonce failed: %v", err)
	}

	bytes := nonce.ToBytes()
	if len(bytes) != fileNonceSize {
		t.Errorf("expected %d bytes, got %d", fileNonceSize, len(bytes))
	}

	// Verify bytes match nonce
	for i := 0; i < fileNonceSize; i++ {
		if bytes[i] != nonce[i] {
			t.Errorf("byte %d mismatch: expected %d, got %d", i, nonce[i], bytes[i])
		}
	}
}

func TestNonce_ToString(t *testing.T) {
	nonce, err := GenerateRandomNonce()
	if err != nil {
		t.Fatalf("GenerateRandomNonce failed: %v", err)
	}

	str := nonce.ToString()
	if len(str) != fileNonceSize {
		t.Errorf("expected string length %d, got %d", fileNonceSize, len(str))
	}
}

func TestNonce_ToBytes_ReturnsSlice(t *testing.T) {
	var n nonce
	for i := range n {
		n[i] = byte(i)
	}

	bytes := n.ToBytes()

	// Verify it returns a proper slice
	if cap(bytes) < fileNonceSize {
		t.Errorf("expected capacity at least %d, got %d", fileNonceSize, cap(bytes))
	}
}

func TestNonce_ConsistentValues(t *testing.T) {
	// Create a nonce with known values
	var n nonce
	for i := range n {
		n[i] = byte(i + 65) // A, B, C, D...
	}

	// ToBytes should return the same values
	bytes := n.ToBytes()
	for i := 0; i < fileNonceSize; i++ {
		expected := byte(i + 65)
		if bytes[i] != expected {
			t.Errorf("byte %d: expected %d, got %d", i, expected, bytes[i])
		}
	}

	// ToString should return the same string representation
	str := n.ToString()
	for i := 0; i < fileNonceSize; i++ {
		expected := byte(i + 65)
		if str[i] != expected {
			t.Errorf("string char %d: expected %c, got %c", i, expected, str[i])
		}
	}
}

func TestGenerateRandomNonce_ContainsAlphanumeric(t *testing.T) {
	nonce, err := GenerateRandomNonce()
	if err != nil {
		t.Fatalf("GenerateRandomNonce failed: %v", err)
	}

	// The password generator should include alphanumeric characters
	hasLetter := false
	hasDigit := false

	for _, b := range nonce {
		if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') {
			hasLetter = true
		}
		if b >= '0' && b <= '9' {
			hasDigit = true
		}
	}

	if !hasLetter {
		t.Error("nonce should contain at least one letter")
	}
	if !hasDigit {
		t.Error("nonce should contain at least one digit")
	}
}
