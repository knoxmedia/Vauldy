package config

import (
	"reflect"
	"testing"
)

func TestWidevineProxyConfigNotExposed(t *testing.T) {
	t.Parallel()

	configType := reflect.TypeOf((*Config)(nil))
	for _, method := range []string{
		"WidevineProxyEnabled",
		"WidevineProxyHeaders",
		"WidevineProxyURL",
		"WidevineProxyTimeout",
	} {
		if _, ok := configType.MethodByName(method); ok {
			t.Fatalf("proxy helper %q should not be exposed", method)
		}
	}

	widevineType := reflect.TypeOf(WidevineConfig{})
	for _, field := range []string{
		"LicenseServerURL",
		"ExtraHeaders",
		"TimeoutSeconds",
	} {
		if _, ok := widevineType.FieldByName(field); ok {
			t.Fatalf("legacy proxy field %q should not exist", field)
		}
	}
}
