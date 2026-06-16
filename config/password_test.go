package config

import "testing"

func TestPasswordPolicyValidate(t *testing.T) {
	tests := []struct {
		name    string
		policy  PasswordPolicy
		pw      string
		wantErr bool
	}{
		{"default min length ok", PasswordPolicy{MinLength: 8}, "password1", false},
		{"default too short", PasswordPolicy{MinLength: 8}, "short", true},
		{"zero min falls back to 8", PasswordPolicy{}, "abcdefgh", false},
		{"zero min rejects 7", PasswordPolicy{}, "abcdefg", true},
		{"require upper missing", PasswordPolicy{MinLength: 4, RequireUppercase: true}, "lower", true},
		{"require upper present", PasswordPolicy{MinLength: 4, RequireUppercase: true}, "Upper", false},
		{"require digit missing", PasswordPolicy{MinLength: 4, RequireDigit: true}, "abcd", true},
		{"require digit present", PasswordPolicy{MinLength: 4, RequireDigit: true}, "abc1", false},
		{"require symbol present", PasswordPolicy{MinLength: 4, RequireSymbol: true}, "ab!d", false},
		{"all rules satisfied", PasswordPolicy{MinLength: 8, RequireUppercase: true, RequireLowercase: true, RequireDigit: true, RequireSymbol: true}, "Abcdef1!", false},
		{"all rules one missing", PasswordPolicy{MinLength: 8, RequireUppercase: true, RequireLowercase: true, RequireDigit: true, RequireSymbol: true}, "Abcdefg1", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.policy.Validate(tt.pw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate(%q) err=%v, wantErr=%v", tt.pw, err, tt.wantErr)
			}
		})
	}
}

func TestInternalAuthPasswordPolicyDefaults(t *testing.T) {
	p := InternalAuthConfig{}.PasswordPolicy()
	if p.MinLength != 8 {
		t.Errorf("default MinLength = %d, want 8", p.MinLength)
	}
}
