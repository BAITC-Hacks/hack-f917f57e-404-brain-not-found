package careerquest

import "embed"

// Templates and public assets ship with the executable. Only web/static is
// exposed by the HTTP file server.
//
//go:embed web/templates/*.html web/static/* web/translations.json
var assets embed.FS
