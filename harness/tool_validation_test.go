package harness

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateToolArgumentsRejectsMalformedAndMissingRequired(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"experiment":{"type":"string","enum":["scan"]},"start":{"type":"integer","minimum":0}},"required":["experiment","start"]}`)
	for _, test := range []struct {
		name string
		args string
		want string
	}{
		{name: "malformed", args: `{"experiment":`, want: "valid JSON"},
		{name: "missing", args: `{"experiment":"scan"}`, want: `"start" is required`},
		{name: "wrong type", args: `{"experiment":"scan","start":"0"}`, want: `"start" must be integer`},
		{name: "bad enum", args: `{"experiment":"other","start":0}`, want: `"experiment" is not an allowed value`},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateToolArguments(schema, json.RawMessage(test.args))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateToolArguments(%s) error=%v, want text %q", test.args, err, test.want)
			}
		})
	}
}
