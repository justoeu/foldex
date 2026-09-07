package backupstatus

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"foldex/internal/backupjobs"
)

// TestScheduleResponseHasATypedWireShape is BP-MEN-005: GET /schedule used to
// encode map[string]any against BackupScheduleResponse. A dropped key (the
// one that happened was agent_schema_version) compiled, shipped, and left
// the band unable to name build skew. The named struct is the contract.
func TestScheduleResponseHasATypedWireShape(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "schedule_handler.go", nil, 0)
	require.NoError(t, err)

	var get *ast.FuncDecl
	var spec *ast.TypeSpec
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncDecl:
			if n.Name.Name == "GetSchedule" {
				get = n
			}
		case *ast.TypeSpec:
			if n.Name.Name == "scheduleResponse" {
				spec = n
			}
		}
		return true
	})
	require.NotNil(t, get, "GetSchedule must exist")
	assert.False(t, encodesOpenMap(get),
		"GetSchedule encodes map[string]any (or equivalent untyped) against a typed TS contract")

	require.NotNil(t, spec, "GetSchedule must encode a named scheduleResponse struct, not map[string]any")
	st, ok := spec.Type.(*ast.StructType)
	require.True(t, ok, "scheduleResponse must be a struct")

	tags := map[string]string{}
	for _, field := range st.Fields.List {
		if field.Tag == nil {
			continue
		}
		raw, err := strconv.Unquote(field.Tag.Value)
		require.NoError(t, err)
		name := jsonTagName(raw)
		if name == "" || name == "-" {
			continue
		}
		tags[name] = raw
	}
	for _, want := range []string{"jobs", "rows", "bounds", "agent", "agent_schema_version"} {
		require.Contains(t, tags, want, "scheduleResponse JSON is missing %q", want)
	}
	assert.NotContains(t, tags["agent_schema_version"], "omitempty",
		"omitempty on agent_schema_version would drop the pin the client compares")

	raw, err := json.Marshal(scheduleResponse{
		Jobs:               Jobs,
		Rows:               map[string]backupjobs.ScheduleRow{},
		Bounds:             scheduleBounds{TimesMin: backupjobs.MinTimes},
		AgentSchemaVersion: backupjobs.RequiredSchemaVersion,
	})
	require.NoError(t, err)
	var wire map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &wire))
	_, ok = wire["agent_schema_version"]
	require.True(t, ok, "scheduleResponse JSON is missing agent_schema_version")

	var got scheduleResponse
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, backupjobs.RequiredSchemaVersion, got.AgentSchemaVersion)

	var missing scheduleResponse
	require.NoError(t, json.Unmarshal([]byte(`{"jobs":[],"rows":{},"bounds":{},"agent":null}`), &missing))
	assert.Zero(t, missing.AgentSchemaVersion,
		"a payload without agent_schema_version must not look like a live schema pin")
}

func encodesOpenMap(fn *ast.FuncDecl) bool {
	if fn == nil || fn.Body == nil {
		return false
	}
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		mt, ok := cl.Type.(*ast.MapType)
		if !ok {
			return true
		}
		key, ok := mt.Key.(*ast.Ident)
		if !ok || key.Name != "string" {
			return true
		}
		switch val := mt.Value.(type) {
		case *ast.Ident:
			if val.Name == "any" {
				found = true
			}
		case *ast.InterfaceType:
			found = true
		}
		return true
	})
	return found
}

func jsonTagName(tag string) string {
	const prefix = `json:"`
	i := strings.Index(tag, prefix)
	if i < 0 {
		return ""
	}
	rest := tag[i+len(prefix):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return ""
	}
	name, _, _ := strings.Cut(rest[:end], ",")
	return name
}
