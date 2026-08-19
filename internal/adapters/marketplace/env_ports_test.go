package marketplace

import (
	"reflect"
	"testing"
)

func TestCatalogParsePortDefaultsReturnsOnlyCuratedPorts(t *testing.T) {
	contents := []byte(`
# Marketplace development defaults
DEED_ORGANIZER_PORT=7002
DEED_NONPROFIT_SERVICE_PORT="4016" # quoted value
AWS_SECRET_ACCESS_KEY=must-never-leave-the-parser
DEED_DATABASE_PASSWORD=also-must-never-leave-the-parser
DEED_WALLET_PORT=4017
`)

	defaults, err := DefaultCatalog().ParsePortDefaults(contents)
	if err != nil {
		t.Fatal(err)
	}
	want := []PortDefault{
		{EnvironmentVariable: "DEED_NONPROFIT_SERVICE_PORT", Port: 4016},
		{EnvironmentVariable: "DEED_ORGANIZER_PORT", Port: 7002},
		{EnvironmentVariable: "DEED_WALLET_PORT", Port: 4017},
	}
	if !reflect.DeepEqual(defaults, want) {
		t.Fatalf("defaults: got %#v, want %#v", defaults, want)
	}
	for _, portDefault := range defaults {
		if portDefault.EnvironmentVariable != "DEED_NONPROFIT_SERVICE_PORT" &&
			portDefault.EnvironmentVariable != "DEED_ORGANIZER_PORT" &&
			portDefault.EnvironmentVariable != "DEED_WALLET_PORT" {
			t.Fatalf("unrelated data escaped: %#v", portDefault)
		}
	}
}

func TestParsePortDefaultsSupportsQuotesCommentsAndExport(t *testing.T) {
	contents := []byte("export DEED_ALPHA_PORT='1001' # comment\r\nDEED_BETA_PORT=2002#comment\n")
	defaults, err := ParsePortDefaults(contents, map[string]struct{}{
		"DEED_ALPHA_PORT": {},
		"DEED_BETA_PORT":  {},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []PortDefault{
		{EnvironmentVariable: "DEED_ALPHA_PORT", Port: 1001},
		{EnvironmentVariable: "DEED_BETA_PORT", Port: 2002},
	}
	if !reflect.DeepEqual(defaults, want) {
		t.Fatalf("defaults: got %#v, want %#v", defaults, want)
	}
}

func TestParsePortDefaultsRejectsDuplicateAndInvalidValues(t *testing.T) {
	tests := map[string]string{
		"duplicate":        "DEED_ALPHA_PORT=1001\nDEED_ALPHA_PORT=1002\n",
		"empty":            "DEED_ALPHA_PORT=\n",
		"negative":         "DEED_ALPHA_PORT=-1\n",
		"expansion":        "DEED_ALPHA_PORT=$OTHER_PORT\n",
		"shell expression": "DEED_ALPHA_PORT=$(command)\n",
		"too high":         "DEED_ALPHA_PORT=65536\n",
		"unterminated":     "DEED_ALPHA_PORT='1001\n",
		"trailing data":    "DEED_ALPHA_PORT='1001' nope\n",
	}
	whitelist := map[string]struct{}{"DEED_ALPHA_PORT": {}}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParsePortDefaults([]byte(contents), whitelist); err == nil {
				t.Fatal("expected invalid dotenv data to fail")
			}
		})
	}
}

func TestParsePortDefaultsIgnoresMalformedUnrelatedData(t *testing.T) {
	contents := []byte("SECRET_WITHOUT_ASSIGNMENT\nPASSWORD='unterminated\nDEED_ALPHA_PORT=1001\n")
	defaults, err := ParsePortDefaults(contents, map[string]struct{}{"DEED_ALPHA_PORT": {}})
	if err != nil {
		t.Fatal(err)
	}
	want := []PortDefault{{EnvironmentVariable: "DEED_ALPHA_PORT", Port: 1001}}
	if !reflect.DeepEqual(defaults, want) {
		t.Fatalf("defaults: got %#v, want %#v", defaults, want)
	}
}

func TestParsePortDefaultsRejectsInvalidWhitelist(t *testing.T) {
	_, err := ParsePortDefaults(nil, map[string]struct{}{"AWS_SECRET_ACCESS_KEY": {}})
	if err == nil {
		t.Fatal("expected invalid whitelist to fail")
	}
}
