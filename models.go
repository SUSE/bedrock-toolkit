// Package bedrocktoolkit holds the files built into the bedrock-admin binary.
package bedrocktoolkit

import _ "embed"

// ModelsTOML is models.toml: the baseline models personal roles may always call when models: lists any.
//
//go:embed models.toml
var ModelsTOML []byte
