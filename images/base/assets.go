package baseassets

import "embed"

// Files contains the self-contained build context for the managed guest image.
//
//go:embed Dockerfile isolated-dev-dockerd
var Files embed.FS
