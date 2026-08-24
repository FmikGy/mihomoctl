package cli_test

import (
	"mihomoctl/internal/app"
	"mihomoctl/internal/cli"
)

var _ cli.Backend = (*app.App)(nil)
var _ cli.TUIRunner = (*app.App)(nil)
