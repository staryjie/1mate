package auth

import "testing"

func TestHashAndVerify(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "correct horse battery staple") {
		t.Fatal("valid password rejected")
	}
	if VerifyPassword(hash, "wrong password here") {
		t.Fatal("invalid password accepted")
	}
	if VerifyPassword("garbage", "correct horse battery staple") {
		t.Fatal("garbage hash accepted")
	}
}

func TestHashPasswordTooShort(t *testing.T) {
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateUsername(t *testing.T) {
	for _, ok := range []string{"admin", "dev-ops", "a_b.c"} {
		if err := ValidateUsername(ok); err != nil {
			t.Fatalf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"ab", "-admin", "ad min", "管理员"} {
		if err := ValidateUsername(bad); err == nil {
			t.Fatalf("%q should be rejected", bad)
		}
	}
}

func TestHashTokenStable(t *testing.T) {
	if HashToken("token") != HashToken("token") {
		t.Fatal("hash must be deterministic")
	}
	if HashToken("token") == HashToken("other") {
		t.Fatal("different tokens must hash differently")
	}
}
