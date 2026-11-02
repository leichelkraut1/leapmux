package workflow

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// bundleFormatVersion is the version of the import/export document. An
// import refuses a newer one rather than dropping fields it cannot read.
const bundleFormatVersion = 1

// maxDocumentBytes bounds an imported document.
const maxDocumentBytes = 1 << 20

//go:embed example_ios.yaml
var exampleBundle string

// Library is the saved profiles and templates of one user on this worker.
func (e *Engine) Library(ctx context.Context, userID string) (*leapmuxv1.ListWorkflowLibraryResponse, error) {
	profiles, err := e.store.ListProfiles(ctx, userID)
	if err != nil {
		return nil, err
	}
	templates, err := e.store.ListTemplates(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &leapmuxv1.ListWorkflowLibraryResponse{Profiles: profiles, Templates: templates}, nil
}

// SaveProfile creates a profile (no id) or replaces one.
func (e *Engine) SaveProfile(ctx context.Context, userID string, p *leapmuxv1.AgentProfile) (*leapmuxv1.AgentProfile, error) {
	p = proto.Clone(p).(*leapmuxv1.AgentProfile)
	if err := NormalizeProfile(p); err != nil {
		return nil, err
	}
	if p.GetId() == "" {
		p.Id = e.newID()
	}
	p.UpdatedAtMs = e.nowMs()
	if err := e.store.PutProfile(ctx, userID, p); err != nil {
		return nil, err
	}
	return p, nil
}

func (e *Engine) DeleteProfile(ctx context.Context, userID, id string) error {
	templates, err := e.store.ListTemplates(ctx, userID)
	if err != nil {
		return err
	}
	for _, t := range templates {
		for _, r := range t.GetRoles() {
			if r.GetProfileId() == id {
				return conflictf("template %q still uses this profile for its %s role", t.GetName(), r.GetName())
			}
		}
	}
	return e.store.DeleteProfile(ctx, userID, id)
}

// SaveTemplate creates a template (no id) or saves its next version. Every
// profile a role references must exist in this library, so a launch can
// resolve it.
func (e *Engine) SaveTemplate(ctx context.Context, userID string, t *leapmuxv1.WorkflowTemplate) (*leapmuxv1.WorkflowTemplate, error) {
	t = proto.Clone(t).(*leapmuxv1.WorkflowTemplate)
	if err := NormalizeTemplate(t); err != nil {
		return nil, err
	}
	profiles, err := e.store.ListProfiles(ctx, userID)
	if err != nil {
		return nil, err
	}
	if _, err := ResolveRoles(t, profiles, nil); err != nil {
		return nil, err
	}
	if t.GetId() == "" {
		t.Id = e.newID()
	}
	t.UpdatedAtMs = e.nowMs()
	if err := e.store.PutTemplate(ctx, userID, t); err != nil {
		return nil, err
	}
	return t, nil
}

func (e *Engine) DeleteTemplate(ctx context.Context, userID, id string) error {
	return e.store.DeleteTemplate(ctx, userID, id)
}

// Export renders a template and the profiles it references as a document a
// person can read, edit and import elsewhere.
func (e *Engine) Export(ctx context.Context, userID, templateID string, format leapmuxv1.WorkflowExportFormat) (*leapmuxv1.ExportWorkflowTemplateResponse, error) {
	t, err := e.store.GetTemplate(ctx, userID, templateID)
	if err != nil {
		return nil, err
	}
	profiles, err := e.store.ListProfiles(ctx, userID)
	if err != nil {
		return nil, err
	}
	bundle := &leapmuxv1.WorkflowTemplateBundle{FormatVersion: bundleFormatVersion, Template: t}
	used := map[string]bool{}
	for _, r := range t.GetRoles() {
		used[r.GetProfileId()] = true
	}
	for _, p := range profiles {
		if used[p.GetId()] {
			bundle.Profiles = append(bundle.Profiles, p)
		}
	}
	doc, err := EncodeBundle(bundle, format)
	if err != nil {
		return nil, err
	}
	ext := "yaml"
	if format == leapmuxv1.WorkflowExportFormat_WORKFLOW_EXPORT_FORMAT_JSON {
		ext = "json"
	}
	return &leapmuxv1.ExportWorkflowTemplateResponse{Document: doc, FileName: fileSlug(t.GetName()) + ".workflow." + ext}, nil
}

var slugJunk = regexp.MustCompile(`[^a-z0-9]+`)

func fileSlug(name string) string {
	slug := strings.Trim(slugJunk.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if slug == "" {
		return "workflow"
	}
	return slug
}

// EncodeBundle renders a bundle as YAML (the default) or JSON, with proto
// field names, and without the library's own timestamps and versions, which
// would only describe this computer.
func EncodeBundle(b *leapmuxv1.WorkflowTemplateBundle, format leapmuxv1.WorkflowExportFormat) (string, error) {
	b = proto.Clone(b).(*leapmuxv1.WorkflowTemplateBundle)
	b.GetTemplate().Version, b.GetTemplate().UpdatedAtMs = 0, 0
	for _, p := range b.GetProfiles() {
		p.UpdatedAtMs = 0
	}
	data, err := protojson.MarshalOptions{UseProtoNames: true, Indent: "  "}.Marshal(b)
	if err != nil {
		return "", err
	}
	if format == leapmuxv1.WorkflowExportFormat_WORKFLOW_EXPORT_FORMAT_JSON {
		return string(data) + "\n", nil
	}
	var tree any
	if err := json.Unmarshal(data, &tree); err != nil {
		return "", err
	}
	var out strings.Builder
	out.WriteString("# LeapMux workflow template. Import it from the Workflows dialog.\n")
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(tree); err != nil {
		return "", err
	}
	return out.String(), enc.Close()
}

// DecodeBundle reads a YAML or JSON bundle.
func DecodeBundle(doc string) (*leapmuxv1.WorkflowTemplateBundle, error) {
	if len(doc) > maxDocumentBytes {
		return nil, invalidf("the document is larger than %d bytes", maxDocumentBytes)
	}
	trimmed := strings.TrimSpace(doc)
	if trimmed == "" {
		return nil, invalidf("the document is empty")
	}
	data := []byte(trimmed)
	if !strings.HasPrefix(trimmed, "{") {
		var tree any
		if err := yaml.Unmarshal(data, &tree); err != nil {
			return nil, invalidf("the document is neither JSON nor valid YAML: %v", err)
		}
		var err error
		if data, err = json.Marshal(tree); err != nil {
			return nil, invalidf("the YAML holds a value JSON cannot represent: %v", err)
		}
	}
	b := &leapmuxv1.WorkflowTemplateBundle{}
	if err := (protojson.UnmarshalOptions{}).Unmarshal(data, b); err != nil {
		return nil, invalidf("the document does not describe a workflow template: %v", err)
	}
	if b.GetFormatVersion() > bundleFormatVersion {
		return nil, invalidf("the document uses format version %d; this LeapMux reads up to %d", b.GetFormatVersion(), bundleFormatVersion)
	}
	if b.GetTemplate() == nil {
		return nil, invalidf("the document has no template")
	}
	return b, nil
}

// Import saves a bundle as a new template with new profiles. Every id is
// fresh, so importing twice yields two independent copies and an import
// never overwrites what the library already holds.
func (e *Engine) Import(ctx context.Context, userID, doc string) (*leapmuxv1.ImportWorkflowTemplateResponse, error) {
	b, err := DecodeBundle(doc)
	if err != nil {
		return nil, err
	}
	return e.importBundle(ctx, userID, b)
}

func (e *Engine) importBundle(ctx context.Context, userID string, b *leapmuxv1.WorkflowTemplateBundle) (*leapmuxv1.ImportWorkflowTemplateResponse, error) {
	remap := map[string]string{}
	for _, p := range b.GetProfiles() {
		if err := NormalizeProfile(p); err != nil {
			return nil, err
		}
		remap[p.GetId()] = e.newID()
	}
	t := b.GetTemplate()
	for _, r := range t.GetRoles() {
		if r.GetProfileId() == "" {
			continue
		}
		fresh, ok := remap[r.GetProfileId()]
		if !ok {
			return nil, invalidf("role %q references profile %q, which the document does not include", r.GetKey(), r.GetProfileId())
		}
		r.ProfileId = fresh
	}
	for _, p := range b.GetProfiles() {
		p.Id = remap[p.GetId()]
	}
	if err := NormalizeTemplate(t); err != nil {
		return nil, err
	}
	if _, err := ResolveRoles(t, b.GetProfiles(), nil); err != nil {
		return nil, err
	}
	out := &leapmuxv1.ImportWorkflowTemplateResponse{}
	for _, p := range b.GetProfiles() {
		p.UpdatedAtMs = e.nowMs()
		if err := e.store.PutProfile(ctx, userID, p); err != nil {
			return nil, fmt.Errorf("save profile %q: %w", p.GetName(), err)
		}
		out.Profiles = append(out.Profiles, p)
	}
	t.Id, t.Version, t.UpdatedAtMs = e.newID(), 0, e.nowMs()
	if err := e.store.PutTemplate(ctx, userID, t); err != nil {
		return nil, err
	}
	out.Template = t
	return out, nil
}

// InstallExample saves the bundled development, audit and iOS testing
// template into the library.
func (e *Engine) InstallExample(ctx context.Context, userID string) (*leapmuxv1.ImportWorkflowTemplateResponse, error) {
	return e.Import(ctx, userID, exampleBundle)
}
