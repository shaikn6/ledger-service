// Package api embeds the OpenAPI specification so the service can serve it.
package api

import _ "embed"

// OpenAPISpec is the raw contents of openapi.yaml.
//
//go:embed openapi.yaml
var OpenAPISpec []byte
