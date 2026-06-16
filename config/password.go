package config

import (
	"fmt"
	"strings"
	"unicode"
)

// PasswordPolicy describes the complexity rules applied to internal-user
// passwords. It is safe to expose to clients so they can show requirements.
type PasswordPolicy struct {
	MinLength        int  `json:"min_length"`
	RequireUppercase bool `json:"require_uppercase"`
	RequireLowercase bool `json:"require_lowercase"`
	RequireDigit     bool `json:"require_digit"`
	RequireSymbol    bool `json:"require_symbol"`
}

// PasswordPolicy builds the effective policy from the config (min length falls
// back to 8 if unset).
func (c InternalAuthConfig) PasswordPolicy() PasswordPolicy {
	min := c.PasswordMinLength
	if min <= 0 {
		min = 8
	}
	return PasswordPolicy{
		MinLength:        min,
		RequireUppercase: c.PasswordRequireUpper,
		RequireLowercase: c.PasswordRequireLower,
		RequireDigit:     c.PasswordRequireDigit,
		RequireSymbol:    c.PasswordRequireSymbol,
	}
}

// DefaultPasswordPolicy is the fallback used when no policy has been configured.
func DefaultPasswordPolicy() PasswordPolicy {
	return PasswordPolicy{MinLength: 8}
}

// Validate returns nil if pw satisfies the policy, otherwise an error that
// describes every unmet requirement.
func (p PasswordPolicy) Validate(pw string) error {
	min := p.MinLength
	if min <= 0 {
		min = 8
	}

	var missing []string
	if len([]rune(pw)) < min {
		missing = append(missing, fmt.Sprintf("be at least %d characters", min))
	}

	var hasUpper, hasLower, hasDigit, hasSymbol bool
	for _, r := range pw {
		switch {
		case unicode.IsUpper(r):
			hasUpper = true
		case unicode.IsLower(r):
			hasLower = true
		case unicode.IsDigit(r):
			hasDigit = true
		case unicode.IsPunct(r) || unicode.IsSymbol(r):
			hasSymbol = true
		}
	}
	if p.RequireUppercase && !hasUpper {
		missing = append(missing, "contain an uppercase letter")
	}
	if p.RequireLowercase && !hasLower {
		missing = append(missing, "contain a lowercase letter")
	}
	if p.RequireDigit && !hasDigit {
		missing = append(missing, "contain a digit")
	}
	if p.RequireSymbol && !hasSymbol {
		missing = append(missing, "contain a symbol")
	}

	if len(missing) > 0 {
		return fmt.Errorf("password must %s", strings.Join(missing, ", "))
	}
	return nil
}
