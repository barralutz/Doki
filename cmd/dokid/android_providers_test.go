package main

import (
	"path/filepath"
	"testing"

	dr "github.com/OpceanAI/Doki/pkg/runtime"
)

func TestRegisterAndroidProvidersForOSRegistersPostgreSQLOnlyOnAndroid(t *testing.T) {
	t.Run("android", func(t *testing.T) {
		reg := dr.NewAndroidProviderRegistry()
		dataDir := t.TempDir()
		if err := registerAndroidProvidersForOS(reg, dataDir, "/termux", "android"); err != nil {
			t.Fatal(err)
		}
		if got := reg.Get("postgresql"); got == nil {
			t.Fatal("postgresql provider not registered for Android")
		}
		_ = filepath.Join(dataDir, "providers", "postgresql")
	})

	t.Run("linux", func(t *testing.T) {
		reg := dr.NewAndroidProviderRegistry()
		if err := registerAndroidProvidersForOS(reg, t.TempDir(), "/termux", "linux"); err != nil {
			t.Fatal(err)
		}
		if got := reg.Get("postgresql"); got != nil {
			t.Fatalf("postgresql provider registered on Linux: %T", got)
		}
	})
}

func TestTermuxPrefixUsesAbsoluteEnvOrAndroidDefault(t *testing.T) {
	t.Setenv("PREFIX", "/custom/termux/usr")
	if got := termuxPrefix(); got != "/custom/termux/usr" {
		t.Fatalf("PREFIX=%q", got)
	}
	t.Setenv("PREFIX", "relative")
	if got := termuxPrefix(); got != "/data/data/com.termux/files/usr" {
		t.Fatalf("relative PREFIX fallback=%q", got)
	}
	t.Setenv("PREFIX", "")
	if got := termuxPrefix(); got != "/data/data/com.termux/files/usr" {
		t.Fatalf("empty PREFIX fallback=%q", got)
	}
}
