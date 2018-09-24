package format

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform/addrs"
	"github.com/hashicorp/terraform/configs/configschema"
	"github.com/hashicorp/terraform/plans"
	"github.com/hashicorp/terraform/states"
	"github.com/hashicorp/terraform/terraform"
	"github.com/mitchellh/colorstring"
)

// StateOpts are the options for formatting a state.
type StateOpts struct {
	// State is the state to format. This is required.
	State *states.State

	// Schemas are used to decode attributes. This is required.
	Schemas *terraform.Schemas

	// Color is the colorizer. This is optional.
	Color *colorstring.Colorize
}

// State takes a state and returns a string
func State(opts *StateOpts) string {
	if opts.Color == nil {
		panic("colorize not given")
	}

	if opts.Schemas == nil {
		panic("schemas not given")
	}

	s := opts.State
	if len(s.Modules) == 0 {
		return "The state file is empty. No resources are represented."
	}

	var buf bytes.Buffer
	buf.WriteString("[reset]")
	p := blockBodyDiffPrinter{
		buf:    &buf,
		color:  opts.Color,
		action: plans.NoOp,
	}

	// Format all the modules
	for _, m := range s.Modules {
		formatStateModule(p, m, opts.Schemas)
	}

	// Write the outputs for the root module
	m := s.RootModule()

	if m.OutputValues != nil {
		if len(m.OutputValues) > 0 {
			p.buf.WriteString("\nOutputs:\n\n")
		}

		// Sort the outputs
		ks := make([]string, 0, len(m.OutputValues))
		for k := range m.OutputValues {
			ks = append(ks, k)
		}
		sort.Strings(ks)

		// Output each output k/v pair
		for _, k := range ks {
			v := m.OutputValues[k]
			p.buf.WriteString(fmt.Sprintf("%s = ", k))
			p.writeValue(v.Value, plans.NoOp, 0)
		}
	}

	return opts.Color.Color(strings.TrimSpace(p.buf.String()))

}

func formatStateModule(
	p blockBodyDiffPrinter, m *states.Module, schemas *terraform.Schemas) {

	var moduleName string
	if !m.Addr.IsRoot() {
		moduleName = fmt.Sprintf("module.%s", m.Addr.String())
	}

	// First get the names of all the resources so we can show them
	// in alphabetical order.
	names := make([]string, 0, len(m.Resources))
	for name := range m.Resources {
		names = append(names, name)
	}
	sort.Strings(names)

	// Go through each resource and begin building up the output.
	for _, key := range names {
		taintStr := ""
		instances := m.Resources[key].Instances
		for k, v := range instances {
			name := key
			if moduleName != "" {
				name = moduleName + "." + name
			}

			addr := m.Resources[key].Addr
			if v.Current.Status == 'T' {
				taintStr = "(tainted)"
			}
			p.buf.WriteString(fmt.Sprintf("# %s: %s\n", addr.Instance(k), taintStr))
			taintStr = ""

			var schema *configschema.Block
			provider := m.Resources[key].ProviderConfig.ProviderConfig.StringCompact()

			switch addr.Mode {
			case addrs.ManagedResourceMode:
				p.buf.WriteString(fmt.Sprintf(
					"resource %q %q {\n",
					addr.Type,
					addr.Name,
				))
				schema = schemas.Providers[provider].ResourceTypes[addr.Type]
			case addrs.DataResourceMode:
				p.buf.WriteString(fmt.Sprintf(
					"data %q %q {\n",
					addr.Type,
					addr.Name,
				))
				schema = schemas.Providers[provider].DataSources[addr.Type]
			default:
				// should never happen, since the above is exhaustive
				p.buf.WriteString(addr.String())
			}

			val, err := v.Current.Decode(schema.ImpliedType())

			if err != nil {
				fmt.Println(err.Error())
				break
			}
			for name := range schema.Attributes {
				attr := ctyGetAttrMaybeNull(val.Value, name)
				if !attr.IsNull() {
					p.buf.WriteString(fmt.Sprintf("    %s = ", name))
					attr := ctyGetAttrMaybeNull(val.Value, name)
					p.writeValue(attr, plans.NoOp, 4)
					p.buf.WriteString("\n")
				}
			}
			p.buf.WriteString("}\n\n")
		}
	}
	p.buf.WriteString("[reset]\n")
}

func formatNestedList(indent string, outputList []interface{}) string {
	outputBuf := new(bytes.Buffer)
	outputBuf.WriteString(fmt.Sprintf("%s[", indent))

	lastIdx := len(outputList) - 1

	for i, value := range outputList {
		outputBuf.WriteString(fmt.Sprintf("\n%s%s%s", indent, "    ", value))
		if i != lastIdx {
			outputBuf.WriteString(",")
		}
	}

	outputBuf.WriteString(fmt.Sprintf("\n%s]", indent))
	return strings.TrimPrefix(outputBuf.String(), "\n")
}

func formatListOutput(indent, outputName string, outputList []interface{}) string {
	keyIndent := ""

	outputBuf := new(bytes.Buffer)

	if outputName != "" {
		outputBuf.WriteString(fmt.Sprintf("%s%s = [", indent, outputName))
		keyIndent = "    "
	}

	lastIdx := len(outputList) - 1

	for i, value := range outputList {
		switch typedValue := value.(type) {
		case string:
			outputBuf.WriteString(fmt.Sprintf("\n%s%s%s", indent, keyIndent, value))
		case []interface{}:
			outputBuf.WriteString(fmt.Sprintf("\n%s%s", indent,
				formatNestedList(indent+keyIndent, typedValue)))
		case map[string]interface{}:
			outputBuf.WriteString(fmt.Sprintf("\n%s%s", indent,
				formatNestedMap(indent+keyIndent, typedValue)))
		}

		if lastIdx != i {
			outputBuf.WriteString(",")
		}
	}

	if outputName != "" {
		if len(outputList) > 0 {
			outputBuf.WriteString(fmt.Sprintf("\n%s]", indent))
		} else {
			outputBuf.WriteString("]")
		}
	}

	return strings.TrimPrefix(outputBuf.String(), "\n")
}

func formatNestedMap(indent string, outputMap map[string]interface{}) string {
	ks := make([]string, 0, len(outputMap))
	for k, _ := range outputMap {
		ks = append(ks, k)
	}
	sort.Strings(ks)

	outputBuf := new(bytes.Buffer)
	outputBuf.WriteString(fmt.Sprintf("%s{", indent))

	lastIdx := len(outputMap) - 1
	for i, k := range ks {
		v := outputMap[k]
		outputBuf.WriteString(fmt.Sprintf("\n%s%s = %v", indent+"    ", k, v))

		if lastIdx != i {
			outputBuf.WriteString(",")
		}
	}

	outputBuf.WriteString(fmt.Sprintf("\n%s}", indent))

	return strings.TrimPrefix(outputBuf.String(), "\n")
}

func formatMapOutput(indent, outputName string, outputMap map[string]interface{}) string {
	ks := make([]string, 0, len(outputMap))
	for k, _ := range outputMap {
		ks = append(ks, k)
	}
	sort.Strings(ks)

	keyIndent := ""

	outputBuf := new(bytes.Buffer)
	if outputName != "" {
		outputBuf.WriteString(fmt.Sprintf("%s%s = {", indent, outputName))
		keyIndent = "  "
	}

	for _, k := range ks {
		v := outputMap[k]
		outputBuf.WriteString(fmt.Sprintf("\n%s%s%s = %v", indent, keyIndent, k, v))
	}

	if outputName != "" {
		if len(outputMap) > 0 {
			outputBuf.WriteString(fmt.Sprintf("\n%s}", indent))
		} else {
			outputBuf.WriteString("}")
		}
	}

	return strings.TrimPrefix(outputBuf.String(), "\n")
}
