package accountrecovery

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode"

	"novastream/config"
	"novastream/internal/datastore"
	"novastream/models"
	"novastream/services/accounts"
	"novastream/services/sessions"

	"github.com/sethvargo/go-password/password"
)

const GeneratedPasswordLength = 24

type Options struct {
	ConfigPath     string
	AccountID      string
	Username       string
	UseMaster      bool
	NewPassword    string
	Generate       bool
	RevokeSessions bool
}

func Run(args []string, stdout io.Writer, getenv func(string) string) error {
	opts, err := ParseFlags(args, getenv)
	if err != nil {
		return err
	}
	return RunWithOptions(opts, stdout, getenv)
}

func ParseFlags(args []string, getenv func(string) string) (Options, error) {
	defaultConfigPath := getenv("STRMR_CONFIG")
	if defaultConfigPath == "" {
		defaultConfigPath = getenv("NOVASTREAM_CONFIG")
	}
	if defaultConfigPath == "" {
		defaultConfigPath = filepath.Join("cache", "settings.json")
	}

	fs := flag.NewFlagSet("recover-account", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	opts := Options{}
	fs.StringVar(&opts.ConfigPath, "config", defaultConfigPath, "path to settings.json")
	fs.StringVar(&opts.AccountID, "account-id", "", "account ID to recover")
	fs.StringVar(&opts.Username, "username", "", "username to recover")
	fs.BoolVar(&opts.UseMaster, "master", false, "target the master account")
	fs.StringVar(&opts.NewPassword, "new-password", "", "new password to set")
	fs.BoolVar(&opts.Generate, "generate", false, "generate a strong password instead of providing one")
	fs.BoolVar(&opts.RevokeSessions, "revoke-sessions", true, "revoke active sessions after reset")

	if err := fs.Parse(args); err != nil {
		return Options{}, err
	}
	if err := ValidateOptions(opts); err != nil {
		return Options{}, err
	}
	return opts, nil
}

func RunWithOptions(opts Options, stdout io.Writer, getenv func(string) string) error {
	settings, err := config.NewManager(opts.ConfigPath).Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	dbURL := strings.TrimSpace(getenv("DATABASE_URL"))
	if dbURL == "" {
		dbURL = strings.TrimSpace(settings.Database.URL)
	}
	if dbURL == "" {
		return errors.New("DATABASE_URL is required for account recovery")
	}

	store, err := datastore.New(context.Background(), dbURL)
	if err != nil {
		return fmt.Errorf("connect datastore: %w", err)
	}
	defer store.Close()

	accountsSvc, err := accounts.NewServiceWithStore(store)
	if err != nil {
		return fmt.Errorf("init accounts service: %w", err)
	}
	sessionsSvc, err := sessions.NewServiceWithStore(store, 0)
	if err != nil {
		return fmt.Errorf("init sessions service: %w", err)
	}

	account, err := ResolveAccount(accountsSvc, opts)
	if err != nil {
		return err
	}

	newPassword := strings.TrimSpace(opts.NewPassword)
	if opts.Generate {
		newPassword, err = GeneratePassword()
		if err != nil {
			return fmt.Errorf("generate password: %w", err)
		}
	}

	if err := accountsSvc.UpdatePassword(account.ID, newPassword); err != nil {
		return fmt.Errorf("update password for %q: %w", account.Username, err)
	}

	revoked := 0
	if opts.RevokeSessions {
		revoked = sessionsSvc.RevokeAllForAccount(account.ID)
	}

	fmt.Fprintf(stdout, "account recovery complete\n")
	fmt.Fprintf(stdout, "account_id: %s\n", account.ID)
	fmt.Fprintf(stdout, "username: %s\n", account.Username)
	fmt.Fprintf(stdout, "is_master: %t\n", account.IsMaster)
	fmt.Fprintf(stdout, "password: %s\n", newPassword)
	fmt.Fprintf(stdout, "revoked_sessions: %d\n", revoked)
	return nil
}

func ValidateOptions(opts Options) error {
	targetCount := 0
	if strings.TrimSpace(opts.AccountID) != "" {
		targetCount++
	}
	if strings.TrimSpace(opts.Username) != "" {
		targetCount++
	}
	if opts.UseMaster {
		targetCount++
	}
	if targetCount != 1 {
		return errors.New("provide exactly one of -account-id, -username, or -master")
	}

	hasProvidedPassword := strings.TrimSpace(opts.NewPassword) != ""
	if hasProvidedPassword == opts.Generate {
		return errors.New("provide exactly one of -new-password or -generate")
	}
	return nil
}

func ResolveAccount(accountsSvc *accounts.Service, opts Options) (models.Account, error) {
	if opts.UseMaster {
		account, ok := accountsSvc.GetMasterAccount()
		if !ok {
			return models.Account{}, errors.New("master account not found")
		}
		return account, nil
	}

	if accountID := strings.TrimSpace(opts.AccountID); accountID != "" {
		account, ok := accountsSvc.Get(accountID)
		if !ok {
			return models.Account{}, fmt.Errorf("account %q not found", accountID)
		}
		return account, nil
	}

	username := strings.TrimSpace(opts.Username)
	account, ok := accountsSvc.GetByUsername(username)
	if !ok {
		return models.Account{}, fmt.Errorf("account with username %q not found", username)
	}
	return account, nil
}

func GeneratePassword() (string, error) {
	pw, err := password.Generate(GeneratedPasswordLength, 6, 6, false, true)
	if err != nil {
		return "", err
	}
	if HasNoLetterOrDigit(pw) {
		return "", errors.New("generated password did not include a letter or digit")
	}
	return pw, nil
}

func HasNoLetterOrDigit(value string) bool {
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
