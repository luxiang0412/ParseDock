package engines

// Capability is the static description of an engine's routing surface.
type Capability struct {
	Name     string
	Device   string
	Formats  []string
	Modes    []string
	Priority int
	Notes    string
}

// BuiltinCapabilities is the single source of truth for routing metadata.
// It must stay in sync with the adapters in this package; `docparse --check`
// prints it so deployments can diff it against worker registrations.
var BuiltinCapabilities = map[string]Capability{
	"native": {
		Name: "native", Device: "cpu", Priority: 100,
		Formats: []string{"txt", "md", "csv", "tsv"}, Modes: []string{"fast", "accurate"},
		Notes: "pure Go; no models, no subprocess; the accurate choice for text formats",
	},
	"mock": {
		Name: "mock", Device: "cpu", Priority: 1,
		Formats: []string{"*"}, Modes: []string{"fast", "accurate", "auto"},
		Notes: "lifecycle testing only",
	},
	"xberg": {
		Name: "xberg", Device: "cpu", Priority: 50,
		Formats: []string{"pdf", "docx", "xlsx", "pptx", "csv", "tsv", "txt", "md", "png", "jpeg", "webp"},
		Modes:   []string{"fast", "auto"}, Notes: "Rust core; native text + tesseract OCR",
	},
	"docling-rs": {
		Name: "docling-rs", Device: "cpu", Priority: 40,
		Formats: []string{"pdf", "docx", "xlsx", "pptx", "html", "md", "txt", "png", "jpeg", "webp"},
		Modes:   []string{"accurate"}, Notes: "Rust Docling pipeline (layout + TableFormer + OCR)",
	},
	"docling-serve": {
		Name: "docling-serve", Device: "both", Priority: 60,
		Formats: []string{"pdf", "docx", "xlsx", "pptx", "html", "md", "png", "jpeg", "webp"},
		Modes:   []string{"accurate"}, Notes: "Python Docling service over HTTP",
	},
	"mineru": {
		Name: "mineru", Device: "both", Priority: 70,
		Formats: []string{"pdf", "png", "jpeg", "webp"}, Modes: []string{"accurate"},
		Notes: "heavy OCR/VLM pipeline",
	},
	"tika": {
		Name: "tika", Device: "cpu", Priority: 10,
		Formats: []string{"pdf", "docx", "xlsx", "pptx", "csv", "tsv", "txt", "md", "png", "jpeg", "webp", "html"},
		Modes:   []string{"fast"}, Notes: "broad-coverage text fallback",
	},
}

// CapabilityFor returns the static capability for an engine name.
func CapabilityFor(name string) (Capability, bool) {
	c, ok := BuiltinCapabilities[name]
	return c, ok
}
