package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func validEnv() map[string]string {
	return map[string]string{
		envInternalAddr: ":8081",
		envPublicAddr:   ":8080",
		envMetricsAddr:  ":9090",
		envJWKSAddr:     ":8083",
		envAuthHost:     "auth.apps.example.test",
		envIssuer:       "https://auth.apps.example.test",
		envKEKFile:      "/run/secrets/kek",
		envFakeLogin:    "true", // the base environment has no Google: see TestLoadConfigGoogle
	}
}

func lookup(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadConfig(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		c, err := loadConfig(lookup(validEnv()))
		if err != nil {
			t.Fatal(err)
		}
		if c.Google != nil || c.IdleTimeout <= 0 || c.AuthHost != "auth.apps.example.test" {
			t.Fatalf("config = %+v", c)
		}
	})
	t.Run("overrides", func(t *testing.T) {
		env := validEnv()
		env[envIdleTimeout] = "30m"
		c, err := loadConfig(lookup(env))
		if err != nil || c.IdleTimeout != 30*time.Minute || !c.FakeLogin {
			t.Fatalf("config = %+v, err %v", c, err)
		}
	})
	for name, tc := range map[string]struct {
		mutate func(map[string]string)
		want   string
	}{
		"missing internal addr":        {func(m map[string]string) { delete(m, envInternalAddr) }, envInternalAddr},
		"missing public addr":          {func(m map[string]string) { delete(m, envPublicAddr) }, envPublicAddr},
		"missing metrics addr":         {func(m map[string]string) { delete(m, envMetricsAddr) }, envMetricsAddr},
		"metrics on the internal addr": {func(m map[string]string) { m[envMetricsAddr] = m[envInternalAddr] }, "different ports"},
		"metrics on the public addr":   {func(m map[string]string) { m[envMetricsAddr] = m[envPublicAddr] }, "different ports"},
		"missing jwks addr":            {func(m map[string]string) { delete(m, envJWKSAddr) }, envJWKSAddr},
		"jwks on the internal addr":    {func(m map[string]string) { m[envJWKSAddr] = m[envInternalAddr] }, "different ports"},
		"jwks on the public addr":      {func(m map[string]string) { m[envJWKSAddr] = m[envPublicAddr] }, "different ports"},
		"jwks on the metrics addr":     {func(m map[string]string) { m[envJWKSAddr] = m[envMetricsAddr] }, "different ports"},
		"missing auth host":            {func(m map[string]string) { delete(m, envAuthHost) }, envAuthHost},
		"missing issuer":               {func(m map[string]string) { delete(m, envIssuer) }, envIssuer},
		"missing KEK file":             {func(m map[string]string) { delete(m, envKEKFile) }, envKEKFile},
		"same addr":                    {func(m map[string]string) { m[envPublicAddr] = m[envInternalAddr] }, "different ports"},
		"auth host with scheme":        {func(m map[string]string) { m[envAuthHost] = "https://auth.apps.example.test" }, envAuthHost},
		"auth host uppercase":          {func(m map[string]string) { m[envAuthHost] = "Auth.Apps.Example.Test" }, envAuthHost},
		"auth host with port":          {func(m map[string]string) { m[envAuthHost] = "auth.apps.example.test:443" }, envAuthHost},
		"issuer other host":            {func(m map[string]string) { m[envIssuer] = "https://other.example.test" }, envIssuer},
		"issuer http":                  {func(m map[string]string) { m[envIssuer] = "http://auth.apps.example.test" }, envIssuer},
		"issuer trailing slash":        {func(m map[string]string) { m[envIssuer] = "https://auth.apps.example.test/" }, envIssuer},
		"idle garbage":                 {func(m map[string]string) { m[envIdleTimeout] = "soon" }, envIdleTimeout},
		"idle zero":                    {func(m map[string]string) { m[envIdleTimeout] = "0s" }, envIdleTimeout},
		"idle below contract":          {func(m map[string]string) { m[envIdleTimeout] = "29m" }, envIdleTimeout},
		"idle above contract":          {func(m map[string]string) { m[envIdleTimeout] = "61m" }, envIdleTimeout},
		"idle beyond absolute":         {func(m map[string]string) { m[envIdleTimeout] = "9h" }, envIdleTimeout},
		"fake login garbage":           {func(m map[string]string) { m[envFakeLogin] = "maybe" }, envFakeLogin},
	} {
		t.Run(name, func(t *testing.T) {
			env := validEnv()
			tc.mutate(env)
			_, err := loadConfig(lookup(env))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want mention of %q", err, tc.want)
			}
		})
	}
	t.Run("portal app id", func(t *testing.T) {
		const id = "app_01ARZ3NDEKTSV4RRFFQ69G5FAV"
		env := validEnv()
		if c, err := loadConfig(lookup(env)); err != nil || c.PortalAppID != "" {
			t.Fatalf("unset: %+v, %v", c, err)
		}
		env[envPortalAppID] = " " + id + " "
		if c, err := loadConfig(lookup(env)); err != nil || c.PortalAppID != id {
			t.Fatalf("valid: %+v, %v", c, err)
		}
		for _, bad := range []string{"app_01arz3ndektsv4rrffq69g5fav", "app_01ARZ3NDEKTSV4RRFFQ69G5FA", "app_01ARZ3NDEKTSV4RRFFQ69G5FAU", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "app_01ARZ3NDEKTSV4RRFFQ69G5FAVX"} {
			env[envPortalAppID] = bad
			if _, err := loadConfig(lookup(env)); err == nil || !strings.Contains(err.Error(), envPortalAppID) {
				t.Fatalf("%q: err = %v, want a rejection", bad, err)
			}
		}
	})
	t.Run("reports every problem at once", func(t *testing.T) {
		_, err := loadConfig(lookup(nil))
		for _, name := range []string{envInternalAddr, envPublicAddr, envMetricsAddr, envJWKSAddr, envAuthHost, envIssuer, envKEKFile} {
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("err = %v, missing %s", err, name)
			}
		}
	})
}

func writeSecret(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "google-secret")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil { // WriteFile is subject to the umask
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigGoogle(t *testing.T) {
	secret := writeSecret(t, "s3cr3t-value\n", 0o640)
	google := func(m map[string]string) {
		m[envGoogleClientID], m[envGoogleClientSecretFile], m[envAllowedHD] = "client-id.apps.googleusercontent.com", secret, "Example.TEST, other.test"
	}

	t.Run("valid", func(t *testing.T) {
		env := validEnv()
		google(env)
		c, err := loadConfig(lookup(env))
		if err != nil || c.Google == nil {
			t.Fatalf("cfg = %+v, err %v", c, err)
		}
		if c.Google.ClientSecret != "s3cr3t-value" || c.Google.ClientID != "client-id.apps.googleusercontent.com" ||
			!reflect.DeepEqual(c.Google.AllowedHD, []string{"example.test", "other.test"}) {
			t.Fatalf("google = %+v", c.Google)
		}
	})
	t.Run("google without fake login", func(t *testing.T) {
		env := validEnv()
		google(env)
		env[envFakeLogin] = "false"
		if c, err := loadConfig(lookup(env)); err != nil || c.Google == nil || c.FakeLogin {
			t.Fatalf("cfg = %+v, err %v", c, err)
		}
	})
	t.Run("fake and google both configured keep both flags: the auth host disables the fake", func(t *testing.T) {
		env := validEnv()
		google(env)
		if c, err := loadConfig(lookup(env)); err != nil || c.Google == nil || !c.FakeLogin {
			t.Fatalf("cfg = %+v, err %v", c, err)
		}
	})
	t.Run("duplicates collapse", func(t *testing.T) {
		env := validEnv()
		google(env)
		env[envAllowedHD] = "a.test,A.test,b.test"
		c, err := loadConfig(lookup(env))
		if err != nil || !reflect.DeepEqual(c.Google.AllowedHD, []string{"a.test", "b.test"}) {
			t.Fatalf("hd = %v, err %v", c.Google, err)
		}
	})
	t.Run("no fake and no google", func(t *testing.T) {
		env := validEnv()
		env[envFakeLogin] = "false"
		if _, err := loadConfig(lookup(env)); err == nil || !strings.Contains(err.Error(), envGoogleClientID) {
			t.Fatalf("err = %v", err)
		}
		delete(env, envFakeLogin) // the default is off
		if _, err := loadConfig(lookup(env)); err == nil || !strings.Contains(err.Error(), envGoogleClientID) {
			t.Fatalf("default: err = %v", err)
		}
	})

	for name, tc := range map[string]struct {
		mutate func(map[string]string)
		want   string
	}{
		"missing client id":     {func(m map[string]string) { delete(m, envGoogleClientID) }, envGoogleClientID},
		"missing secret file":   {func(m map[string]string) { delete(m, envGoogleClientSecretFile) }, envGoogleClientSecretFile},
		"missing hd":            {func(m map[string]string) { delete(m, envAllowedHD) }, envAllowedHD},
		"client id whitespace":  {func(m map[string]string) { m[envGoogleClientID] = "a b" }, envGoogleClientID},
		"secret file not found": {func(m map[string]string) { m[envGoogleClientSecretFile] = filepath.Join(t.TempDir(), "nope") }, envGoogleClientSecretFile},
		"secret file is a dir":  {func(m map[string]string) { m[envGoogleClientSecretFile] = t.TempDir() }, "not a regular file"},
		"secret group-writable": {func(m map[string]string) { m[envGoogleClientSecretFile] = writeSecret(t, "x", 0o660) }, "too open"},
		"secret world-readable": {func(m map[string]string) { m[envGoogleClientSecretFile] = writeSecret(t, "x", 0o644) }, "too open"},
		"secret empty":          {func(m map[string]string) { m[envGoogleClientSecretFile] = writeSecret(t, " \n", 0o600) }, "empty"},
		"secret huge": {func(m map[string]string) {
			m[envGoogleClientSecretFile] = writeSecret(t, strings.Repeat("x", maxClientSecretBytes+1), 0o600)
		}, "too large"},
		"hd empty entry": {func(m map[string]string) { m[envAllowedHD] = "a.test,,b.test" }, envAllowedHD},
		"hd only commas": {func(m map[string]string) { m[envAllowedHD] = " , " }, envAllowedHD},
		"hd with scheme": {func(m map[string]string) { m[envAllowedHD] = "https://a.test" }, envAllowedHD},
		"hd without dot": {func(m map[string]string) { m[envAllowedHD] = "localhost" }, envAllowedHD},
		"hd wildcard":    {func(m map[string]string) { m[envAllowedHD] = "*.a.test" }, envAllowedHD},
	} {
		t.Run(name, func(t *testing.T) {
			env := validEnv()
			google(env)
			tc.mutate(env)
			_, err := loadConfig(lookup(env))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want mention of %q", err, tc.want)
			}
		})
	}
	t.Run("the secret never appears in errors", func(t *testing.T) {
		env := validEnv()
		google(env)
		env[envGoogleClientSecretFile] = writeSecret(t, "top-secret-do-not-print", 0o666)
		_, err := loadConfig(lookup(env))
		if err == nil || strings.Contains(err.Error(), "top-secret-do-not-print") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("partial google config is not a fake-only config", func(t *testing.T) {
		env := validEnv()
		env[envAllowedHD] = "a.test"
		_, err := loadConfig(lookup(env))
		if err == nil || !strings.Contains(err.Error(), envGoogleClientID) || !strings.Contains(err.Error(), envGoogleClientSecretFile) {
			t.Fatalf("err = %v", err)
		}
	})
}
