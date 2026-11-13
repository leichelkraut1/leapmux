package tradinglab

import (
	_ "embed"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/workflow"
)

// ExampleName is the bundled template's name in the Workflows dialog.
const ExampleName = "blind-trading-lab"

//go:embed blind_trading_lab.yaml
var templateYAML string

// stepSchemas are the output schemas the research steps are validated by,
// filled into the template so its YAML need not repeat them.
var stepSchemas = map[string]*string{
	defaultBlindStep:      &PredictionSchema,
	defaultContrarianStep: &PredictionSchema,
	defaultEvaluationStep: &EvaluationSchema,
	defaultAuditStep:      &AuditSchema,
}

// TemplateBundle is the blind-trading-lab template and its profiles.
func TemplateBundle() (*leapmuxv1.WorkflowTemplateBundle, error) {
	b, err := workflow.DecodeBundle(templateYAML)
	if err != nil {
		return nil, fmt.Errorf("the bundled blind-trading-lab template: %w", err)
	}
	for _, s := range b.GetTemplate().GetSteps() {
		if schema := stepSchemas[s.GetKey()]; schema != nil {
			s.OutputSchema = *schema
		}
	}
	return b, nil
}

// RegisterExample offers the template in the Workflows dialog.
func RegisterExample() {
	workflow.RegisterExample(workflow.Example{
		Info: &leapmuxv1.WorkflowExample{
			Name: ExampleName, Title: "Blind trading research",
			Description: "Isolated analysts research one frozen options snapshot; anonymized scoring, a risk audit and three shadow portfolios. Paper-only until you confirm an exact order.",
		},
		Bundle: TemplateBundle,
	})
}
