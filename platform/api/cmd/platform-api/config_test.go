package main

import (
	"strings"
	"testing"
	"time"
)

func validEnv() map[string]string {
	return map[string]string{
		envInternalAddr: ":8081",
		envPublicAddr:   ":8080",
		envAuthHost:     "auth.apps.example.test",
		envIssuer:       "https://auth.apps.example.test",
		envKEKFile:      "/run/secrets/kek",
	}
}

func lookup(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadConfig(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		c, err := loadConfig(lookup(validEnv()))
		if err != nil {
			t.Fatal(err)
		}
		if c.FakeLogin || c.IdleTimeout <= 0 || c.AuthHost != "auth.apps.example.test" {
			t.Fatalf("config = %+v", c)
		}
	})
	t.Run("overrides", func(t *testing.T) {
		env := validEnv()
		env[envIdleTimeout], env[envFakeLogin] = "30m", "true"
		c, err := loadConfig(lookup(env))
		if err != nil || c.IdleTimeout != 30*time.Minute || !c.FakeLogin {
			t.Fatalf("config = %+v, err %v", c, err)
		}
	})
	for name, tc := range map[string]struct {
		mutate func(map[string]string)
		want   string
	}{
		"missing internal addr": {func(m map[string]string) { delete(m, envInternalAddr) }, envInternalAddr},
		"missing public addr":   {func(m map[string]string) { delete(m, envPublicAddr) }, envPublicAddr},
		"missing auth host":     {func(m map[string]string) { delete(m, envAuthHost) }, envAuthHost},
		"missing issuer":        {func(m map[string]string) { delete(m, envIssuer) }, envIssuer},
		"missing KEK file":      {func(m map[string]string) { delete(m, envKEKFile) }, envKEKFile},
		"same addr":             {func(m map[string]string) { m[envPublicAddr] = m[envInternalAddr] }, "must differ"},
		"auth host with scheme": {func(m map[string]string) { m[envAuthHost] = "https://auth.apps.example.test" }, envAuthHost},
		"auth host uppercase":   {func(m map[string]string) { m[envAuthHost] = "Auth.Apps.Example.Test" }, envAuthHost},
		"auth host with port":   {func(m map[string]string) { m[envAuthHost] = "auth.apps.example.test:443" }, envAuthHost},
		"issuer other host":     {func(m map[string]string) { m[envIssuer] = "https://other.example.test" }, envIssuer},
		"issuer http":           {func(m map[string]string) { m[envIssuer] = "http://auth.apps.example.test" }, envIssuer},
		"issuer trailing slash": {func(m map[string]string) { m[envIssuer] = "https://auth.apps.example.test/" }, envIssuer},
		"idle garbage":          {func(m map[string]string) { m[envIdleTimeout] = "soon" }, envIdleTimeout},
		"idle zero":             {func(m map[string]string) { m[envIdleTimeout] = "0s" }, envIdleTimeout},
		"idle below contract":   {func(m map[string]string) { m[envIdleTimeout] = "29m" }, envIdleTimeout},
		"idle above contract":   {func(m map[string]string) { m[envIdleTimeout] = "61m" }, envIdleTimeout},
		"idle beyond absolute":  {func(m map[string]string) { m[envIdleTimeout] = "9h" }, envIdleTimeout},
		"fake login garbage":    {func(m map[string]string) { m[envFakeLogin] = "maybe" }, envFakeLogin},
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
	t.Run("reports every problem at once", func(t *testing.T) {
		_, err := loadConfig(lookup(nil))
		for _, name := range []string{envInternalAddr, envPublicAddr, envAuthHost, envIssuer, envKEKFile} {
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("err = %v, missing %s", err, name)
			}
		}
	})
}
