package documentir

import _ "embed"

// SchemaJSON is the normative JSON Schema for doc-ir/0.1. The canonical file
// lives next to this package; `make schema` publishes a copy to api/ and CI fails
// when the two differ.
//
//go:embed schema/document-ir-0.1.schema.json
var SchemaJSON []byte
