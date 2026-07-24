// Package bootstrap contains first-run initialization tasks.
package bootstrap

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/headercat/airbrew/internal/auth/user"
	"github.com/headercat/airbrew/internal/config"
)

// EnsureAdmin creates an administrator account when none exists. If a password
// is generated, it is printed once to stdout and is never recoverable later.
func EnsureAdmin(ctx context.Context, db *sql.DB, cfg config.Config, logger *slog.Logger) error {
	repo := user.NewRepository(db)
	count, err := repo.CountByRole(ctx, user.RoleAdmin)
	if err != nil {
		return fmt.Errorf("bootstrap: count admin users: %w", err)
	}
	logger.Info("bootstrap admin check", "existing_admin_count", count)
	if count > 0 {
		return nil
	}

	adminEmail := strings.TrimSpace(cfg.BootstrapAdminEmail)
	if adminEmail == "" {
		adminEmail = "admin@airbrew.local"
	}

	adminPassword := cfg.BootstrapAdminPassword
	generatedPassword := false
	if adminPassword == "" {
		var err error
		adminPassword, err = generatePassword()
		if err != nil {
			return fmt.Errorf("bootstrap: generate admin password: %w", err)
		}
		generatedPassword = true
	}

	svc := user.NewService(repo)
	admin, err := svc.RegisterAdmin(ctx, adminEmail, adminPassword, "Administrator")
	if err != nil {
		return fmt.Errorf("bootstrap: create admin user: %w", err)
	}

	printAdminCredentials(admin.Email, adminPassword, admin.ID, generatedPassword)
	logger.Info("bootstrap admin created", "email", admin.Email, "user_id", admin.ID)
	return nil
}

func generatePassword() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func printAdminCredentials(email, password, userID string, generated bool) {
	line := strings.Repeat("=", 72)
	f := os.Stdout
	fmt.Fprintln(f)
	fmt.Fprintln(f, line)
	fmt.Fprintln(f, " AIRBREW BOOTSTRAP ADMIN CREATED")
	fmt.Fprintln(f, line)
	fmt.Fprintf(f, " Email:    %s\n", email)
	fmt.Fprintf(f, " Password: %s\n", password)
	fmt.Fprintf(f, " User ID:  %s\n", userID)
	if generated {
		fmt.Fprintln(f, " Note:     Save this generated password now. It will not be shown again.")
	} else {
		fmt.Fprintln(f, " Note:     Password came from AIRBREW_BOOTSTRAP_ADMIN_PASSWORD.")
	}
	fmt.Fprintln(f, line)
	fmt.Fprintln(f)
}
