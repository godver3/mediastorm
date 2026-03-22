package accountrecovery

import "testing"

func TestValidateOptions_RequiresSingleTarget(t *testing.T) {
	err := ValidateOptions(Options{
		Username:    "admin",
		AccountID:   "master",
		NewPassword: "secret123",
	})
	if err == nil {
		t.Fatal("expected validation error for multiple targets")
	}
}

func TestValidateOptions_RequiresPasswordSource(t *testing.T) {
	err := ValidateOptions(Options{
		Username: "admin",
	})
	if err == nil {
		t.Fatal("expected validation error when no password source is provided")
	}
}

func TestValidateOptions_RejectsDualPasswordSources(t *testing.T) {
	err := ValidateOptions(Options{
		Username:    "admin",
		NewPassword: "secret123",
		Generate:    true,
	})
	if err == nil {
		t.Fatal("expected validation error when both password modes are provided")
	}
}

func TestValidateOptions_AcceptsSingleTargetAndGeneratedPassword(t *testing.T) {
	err := ValidateOptions(Options{
		UseMaster: true,
		Generate:  true,
	})
	if err != nil {
		t.Fatalf("expected valid options, got %v", err)
	}
}

func TestGeneratePassword(t *testing.T) {
	pw, err := GeneratePassword()
	if err != nil {
		t.Fatalf("GeneratePassword failed: %v", err)
	}
	if len(pw) != GeneratedPasswordLength {
		t.Fatalf("password length = %d, want %d", len(pw), GeneratedPasswordLength)
	}
	if HasNoLetterOrDigit(pw) {
		t.Fatal("expected generated password to include a letter or digit")
	}
}

func TestHasNoLetterOrDigit(t *testing.T) {
	if !HasNoLetterOrDigit("!@#$%^") {
		t.Fatal("expected symbols-only string to return true")
	}
	if HasNoLetterOrDigit("abc123!") {
		t.Fatal("expected alphanumeric string to return false")
	}
}
