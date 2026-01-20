package rclone

import (
	"bytes"
	"context"
	"io"
	"testing"

	"novastream/internal/encryption"
)

func TestNewRcloneCipher_WithPassword(t *testing.T) {
	config := &encryption.Config{
		RclonePassword: "testpassword",
		RcloneSalt:     "testsalt",
	}

	cipher, err := NewRcloneCipher(config)
	if err != nil {
		t.Fatalf("NewRcloneCipher failed: %v", err)
	}
	if cipher == nil {
		t.Fatal("expected non-nil cipher")
	}
	if !cipher.hasGlobalPassword {
		t.Error("expected hasGlobalPassword to be true")
	}
}

func TestNewRcloneCipher_NoPassword(t *testing.T) {
	config := &encryption.Config{
		RclonePassword: "",
		RcloneSalt:     "",
	}

	cipher, err := NewRcloneCipher(config)
	if err != nil {
		t.Fatalf("NewRcloneCipher failed: %v", err)
	}
	if cipher == nil {
		t.Fatal("expected non-nil cipher")
	}
	if cipher.hasGlobalPassword {
		t.Error("expected hasGlobalPassword to be false")
	}
}

func TestNewRcloneCipher_WithSaltOnly(t *testing.T) {
	config := &encryption.Config{
		RclonePassword: "",
		RcloneSalt:     "customsalt",
	}

	cipher, err := NewRcloneCipher(config)
	if err != nil {
		t.Fatalf("NewRcloneCipher failed: %v", err)
	}
	if cipher == nil {
		t.Fatal("expected non-nil cipher")
	}
}

func TestEncryptedSize_SmallFile(t *testing.T) {
	// For a small file (less than block size), the encrypted size should be:
	// fileHeaderSize (32) + blockHeaderSize (16) + original size
	smallSize := int64(100)
	encrypted := EncryptedSize(smallSize)

	// Should be header (32) + block overhead (16) + data (100) = 148
	expected := int64(fileHeaderSize) + int64(blockHeaderSize) + smallSize
	if encrypted != expected {
		t.Errorf("expected encrypted size %d, got %d", expected, encrypted)
	}
}

func TestEncryptedSize_LargeFile(t *testing.T) {
	// For a file larger than one block (64KB), we need multiple blocks
	// Each block has blockHeaderSize (16) overhead plus blockDataSize (65536) data
	largeSize := int64(100000) // ~100KB, which is more than one block

	encrypted := EncryptedSize(largeSize)

	// Calculate expected: header + full blocks + residue block
	blocks := largeSize / blockDataSize
	residue := largeSize % blockDataSize

	expected := int64(fileHeaderSize) + blocks*(blockHeaderSize+blockDataSize)
	if residue != 0 {
		expected += blockHeaderSize + residue
	}

	if encrypted != expected {
		t.Errorf("expected encrypted size %d, got %d", expected, encrypted)
	}
}

func TestEncryptedSize_BlockBoundary(t *testing.T) {
	// Test exact block boundary
	exactBlockSize := int64(blockDataSize) // Exactly one block of data

	encrypted := EncryptedSize(exactBlockSize)

	// Should be header + one full block
	expected := int64(fileHeaderSize + blockHeaderSize + blockDataSize)
	if encrypted != expected {
		t.Errorf("expected encrypted size %d for exact block, got %d", expected, encrypted)
	}
}

func TestEncryptedSize_ZeroFile(t *testing.T) {
	encrypted := EncryptedSize(0)

	// Zero-size file should only have the header
	expected := int64(fileHeaderSize)
	if encrypted != expected {
		t.Errorf("expected encrypted size %d for zero file, got %d", expected, encrypted)
	}
}

func TestEncryptedSize_MultipleBlocks(t *testing.T) {
	// Test exactly two blocks
	twoBlocks := int64(2 * blockDataSize)

	encrypted := EncryptedSize(twoBlocks)

	// Should be header + two full blocks
	expected := int64(fileHeaderSize + 2*(blockHeaderSize+blockDataSize))
	if encrypted != expected {
		t.Errorf("expected encrypted size %d for two blocks, got %d", expected, encrypted)
	}
}

func TestDecryptedSize_Valid(t *testing.T) {
	// Create a cipher for testing
	cipher, _ := NewCipher(NameEncryptionOff, "", "", false, nil)

	// Test with a valid encrypted size (header + one block with some data)
	encryptedSize := int64(fileHeaderSize + blockHeaderSize + 100)

	decrypted, err := cipher.DecryptedSize(encryptedSize)
	if err != nil {
		t.Fatalf("DecryptedSize failed: %v", err)
	}

	// The decrypted size should be 100 (just the data)
	if decrypted != 100 {
		t.Errorf("expected decrypted size 100, got %d", decrypted)
	}
}

func TestDecryptedSize_TooSmall(t *testing.T) {
	cipher, _ := NewCipher(NameEncryptionOff, "", "", false, nil)

	// Test with a size smaller than header
	tooSmall := int64(10)

	_, err := cipher.DecryptedSize(tooSmall)
	if err == nil {
		t.Error("expected error for size smaller than header")
	}
	if err != ErrorEncryptedFileTooShort {
		t.Errorf("expected ErrorEncryptedFileTooShort, got %v", err)
	}
}

func TestDecryptedSize_ExactHeader(t *testing.T) {
	cipher, _ := NewCipher(NameEncryptionOff, "", "", false, nil)

	// Test with exactly the header size (no data)
	headerOnly := int64(fileHeaderSize)

	decrypted, err := cipher.DecryptedSize(headerOnly)
	if err != nil {
		t.Fatalf("DecryptedSize failed for header only: %v", err)
	}

	// Should be 0 bytes of data
	if decrypted != 0 {
		t.Errorf("expected 0 for header-only file, got %d", decrypted)
	}
}

func TestDecryptedSize_FullBlock(t *testing.T) {
	cipher, _ := NewCipher(NameEncryptionOff, "", "", false, nil)

	// Test with header + one full block
	fullBlock := int64(fileHeaderSize + blockSize)

	decrypted, err := cipher.DecryptedSize(fullBlock)
	if err != nil {
		t.Fatalf("DecryptedSize failed: %v", err)
	}

	// Should be exactly blockDataSize
	if decrypted != blockDataSize {
		t.Errorf("expected %d for full block, got %d", blockDataSize, decrypted)
	}
}

func TestRcloneCrypt_Open_MissingPassword(t *testing.T) {
	config := &encryption.Config{
		RclonePassword: "", // No global password
		RcloneSalt:     "",
	}

	cipher, _ := NewRcloneCipher(config)

	// Try to open without providing a password
	_, err := cipher.Open(
		context.Background(),
		nil,       // no range header
		1000,      // file size
		"",        // no per-file password
		"",        // no salt
		func(ctx context.Context, start, end int64) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader([]byte{})), nil
		},
	)

	if err != ErrMissingPassword {
		t.Errorf("expected ErrMissingPassword, got %v", err)
	}
}

func TestRcloneCrypt_Open_WithGlobalPassword(t *testing.T) {
	config := &encryption.Config{
		RclonePassword: "testpassword",
		RcloneSalt:     "testsalt",
	}

	cipher, _ := NewRcloneCipher(config)

	// Should not return error when global password is set
	reader, err := cipher.Open(
		context.Background(),
		nil,   // no range header
		1000,  // file size
		"",    // no per-file password (use global)
		"",    // no per-file salt
		func(ctx context.Context, start, end int64) (io.ReadCloser, error) {
			// Return a minimal encrypted file structure
			return io.NopCloser(bytes.NewReader([]byte{})), nil
		},
	)

	if err != nil {
		t.Fatalf("Open with global password failed: %v", err)
	}
	if reader == nil {
		t.Error("expected non-nil reader")
	}
	reader.Close()
}

func TestRcloneCrypt_Open_WithPerFilePassword(t *testing.T) {
	config := &encryption.Config{
		RclonePassword: "", // No global password
		RcloneSalt:     "",
	}

	cipher, _ := NewRcloneCipher(config)

	// Should not return error when per-file password is provided
	reader, err := cipher.Open(
		context.Background(),
		nil,              // no range header
		1000,             // file size
		"filepassword",   // per-file password
		"filesalt",       // per-file salt
		func(ctx context.Context, start, end int64) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader([]byte{})), nil
		},
	)

	if err != nil {
		t.Fatalf("Open with per-file password failed: %v", err)
	}
	if reader == nil {
		t.Error("expected non-nil reader")
	}
	reader.Close()
}

func TestRcloneCrypt_OverheadSize(t *testing.T) {
	config := &encryption.Config{
		RclonePassword: "test",
		RcloneSalt:     "",
	}

	cipher, _ := NewRcloneCipher(config)

	testCases := []struct {
		name     string
		fileSize int64
	}{
		{"small file", 100},
		{"one block", blockDataSize},
		{"two blocks", 2 * blockDataSize},
		{"partial block", blockDataSize + 1000},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			overhead := cipher.OverheadSize(tc.fileSize)
			encrypted := cipher.EncryptedSize(tc.fileSize)

			// Overhead should be the difference between encrypted and original
			expectedOverhead := encrypted - tc.fileSize
			if overhead != expectedOverhead {
				t.Errorf("expected overhead %d, got %d", expectedOverhead, overhead)
			}

			// Overhead should always be positive
			if overhead < 0 {
				t.Errorf("overhead should be positive, got %d", overhead)
			}
		})
	}
}

func TestRcloneCrypt_Name(t *testing.T) {
	config := &encryption.Config{
		RclonePassword: "test",
		RcloneSalt:     "",
	}

	cipher, _ := NewRcloneCipher(config)

	name := cipher.Name()
	if name != encryption.RCloneCipherType {
		t.Errorf("expected cipher type %q, got %q", encryption.RCloneCipherType, name)
	}
}

func TestGenerateKey_WithPassword(t *testing.T) {
	key, err := GenerateKey("testpassword", "testsalt")
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}
	if key == nil {
		t.Fatal("expected non-nil key")
	}

	// Verify that the keys are non-zero
	allZero := true
	for _, b := range key.dataKey {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Error("expected non-zero dataKey with password")
	}
}

func TestGenerateKey_EmptyPassword(t *testing.T) {
	key, err := GenerateKey("", "")
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}
	if key == nil {
		t.Fatal("expected non-nil key")
	}

	// With empty password, all keys should be zero
	for _, b := range key.dataKey {
		if b != 0 {
			t.Error("expected all-zero dataKey with empty password")
			break
		}
	}
}

func TestGenerateKey_DifferentSalts(t *testing.T) {
	key1, _ := GenerateKey("password", "salt1")
	key2, _ := GenerateKey("password", "salt2")

	// Different salts should produce different keys
	if key1.dataKey == key2.dataKey {
		t.Error("expected different keys for different salts")
	}
}

func TestGenerateKey_SameInputsSameOutput(t *testing.T) {
	key1, _ := GenerateKey("password", "salt")
	key2, _ := GenerateKey("password", "salt")

	// Same inputs should produce same keys
	if key1.dataKey != key2.dataKey {
		t.Error("expected same keys for same inputs")
	}
}

func TestCipher_NameEncryptionMode(t *testing.T) {
	testCases := []struct {
		mode     NameEncryptionMode
		expected string
	}{
		{NameEncryptionOff, "off"},
		{NameEncryptionStandard, "standard"},
		{NameEncryptionObfuscated, "obfuscate"},
	}

	for _, tc := range testCases {
		t.Run(tc.expected, func(t *testing.T) {
			cipher, _ := NewCipher(tc.mode, "", "", false, nil)
			if cipher.NameEncryptionMode() != tc.mode {
				t.Errorf("expected mode %v, got %v", tc.mode, cipher.NameEncryptionMode())
			}
			if tc.mode.String() != tc.expected {
				t.Errorf("expected mode string %q, got %q", tc.expected, tc.mode.String())
			}
		})
	}
}

func TestNewNameEncryptionMode(t *testing.T) {
	testCases := []struct {
		input    string
		expected NameEncryptionMode
		hasError bool
	}{
		{"off", NameEncryptionOff, false},
		{"standard", NameEncryptionStandard, false},
		{"obfuscate", NameEncryptionObfuscated, false},
		{"OFF", NameEncryptionOff, false},       // case insensitive
		{"Standard", NameEncryptionStandard, false},
		{"invalid", 0, true},
	}

	for _, tc := range testCases {
		t.Run(tc.input, func(t *testing.T) {
			mode, err := NewNameEncryptionMode(tc.input)
			if tc.hasError {
				if err == nil {
					t.Errorf("expected error for input %q", tc.input)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error for input %q: %v", tc.input, err)
				}
				if mode != tc.expected {
					t.Errorf("expected mode %v, got %v", tc.expected, mode)
				}
			}
		})
	}
}

func TestCipher_ChangeGlobalPassword(t *testing.T) {
	cipher, _ := NewCipher(NameEncryptionOff, "", "", false, nil)

	// Initially no global key
	if cipher.globalKey != nil {
		t.Error("expected nil globalKey initially")
	}

	// Change password
	err := cipher.ChangeGlobalPassword("newpassword", "newsalt")
	if err != nil {
		t.Fatalf("ChangeGlobalPassword failed: %v", err)
	}

	// Now should have a global key
	if cipher.globalKey == nil {
		t.Error("expected non-nil globalKey after change")
	}
}

func TestCipher_EncryptFileName_NameEncryptionOff(t *testing.T) {
	cipher, _ := NewCipher(NameEncryptionOff, "", "", false, nil)

	// With name encryption off, should just add suffix
	encrypted := cipher.EncryptFileName("test.txt", nil)
	expected := "test.txt.bin"
	if encrypted != expected {
		t.Errorf("expected %q, got %q", expected, encrypted)
	}
}

func TestCipher_DecryptFileName_NameEncryptionOff(t *testing.T) {
	cipher, _ := NewCipher(NameEncryptionOff, "", "", false, nil)

	// With name encryption off, should just remove suffix
	decrypted, err := cipher.DecryptFileName("test.txt.bin", nil)
	if err != nil {
		t.Fatalf("DecryptFileName failed: %v", err)
	}
	expected := "test.txt"
	if decrypted != expected {
		t.Errorf("expected %q, got %q", expected, decrypted)
	}
}

func TestCipher_DecryptFileName_NotEncrypted(t *testing.T) {
	cipher, _ := NewCipher(NameEncryptionOff, "", "", false, nil)

	// File without .bin suffix should error
	_, err := cipher.DecryptFileName("test.txt", nil)
	if err != ErrorNotAnEncryptedFile {
		t.Errorf("expected ErrorNotAnEncryptedFile, got %v", err)
	}
}

func TestReadFill_FullRead(t *testing.T) {
	data := []byte("hello world")
	reader := bytes.NewReader(data)
	buf := make([]byte, len(data))

	n, err := ReadFill(reader, buf)
	// ReadFill reads until buffer is full OR error; when buffer is exact size
	// of data, it fills the buffer and returns nil error (EOF comes on next read)
	if err != nil && err != io.EOF {
		t.Errorf("unexpected error: %v", err)
	}
	if n != len(data) {
		t.Errorf("expected %d bytes, got %d", len(data), n)
	}
	if string(buf) != "hello world" {
		t.Errorf("expected 'hello world', got %q", string(buf))
	}
}

func TestReadFill_PartialRead(t *testing.T) {
	data := []byte("short")
	reader := bytes.NewReader(data)
	buf := make([]byte, 100) // larger than data

	n, err := ReadFill(reader, buf)
	if err != io.EOF {
		t.Errorf("expected EOF, got %v", err)
	}
	if n != len(data) {
		t.Errorf("expected %d bytes, got %d", len(data), n)
	}
}

func TestNewNameEncoding(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		hasError bool
	}{
		{"base32", "base32", false},
		{"base64", "base64", false},
		{"base32768", "base32768", false},
		{"invalid", "invalid", true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			enc, err := NewNameEncoding(tc.input)
			if tc.hasError {
				if err == nil {
					t.Errorf("expected error for %q", tc.input)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if enc == nil {
				t.Error("expected non-nil encoding")
			}
		})
	}
}
