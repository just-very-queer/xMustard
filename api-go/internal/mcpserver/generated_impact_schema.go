package mcpserver

import (
	_ "embed"
	"encoding/json"
)

// impactSchemaGenerated is emitted from rust-core/src/mcp_schema.rs; the Rust test
// checks it against the generated descriptors.
//go:embed impact_schema_generated.json
var impactSchemaGenerated []byte

type impactSchemaFile struct {
	Args []impactSchemaArg `json:"args"`
}

type impactSchemaArg struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Enum     []string `json:"enum"`
	Min      int      `json:"min"`
	Max      int      `json:"max"`
	MaxLen   int      `json:"max_len"`
	List     bool     `json:"list"`
	Advanced bool     `json:"advanced"`
	Desc     string   `json:"desc"`
}

var impactArgs, impactAdvancedArgs = loadImpactSchema()

func loadImpactSchema() ([]Arg, []Arg) {
	var schema impactSchemaFile
	if err := json.Unmarshal(impactSchemaGenerated, &schema); err != nil {
		panic("invalid generated impact schema: " + err.Error())
	}
	var args, advanced []Arg
	for _, field := range schema.Args {
		arg := Arg{
			Name: field.Name, Type: field.Type, Required: field.Required, Enum: field.Enum,
			Min: field.Min, Max: field.Max, MaxLen: field.MaxLen, List: field.List, Desc: field.Desc,
		}
		if field.Advanced {
			advanced = append(advanced, arg)
		} else {
			args = append(args, arg)
		}
	}
	return args, advanced
}
