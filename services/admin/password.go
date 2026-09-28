package admin

import (
	"github.com/pkg/errors"
	"golang.org/x/crypto/bcrypt"
)

// MinPasswordLength guards against trivially guessable admin passwords.
const MinPasswordLength = 12

// HashPassword returns a bcrypt hash for admin.password_hash.
func HashPassword(password string) (string, error) {
	if len(password) < MinPasswordLength {
		return "", errors.Errorf("the admin password must be at least %d characters", MinPasswordLength)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", errors.Wrap(err, "failed to hash password")
	}
	return string(hash), nil
}
