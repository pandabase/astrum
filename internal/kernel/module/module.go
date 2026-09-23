package module

import (
	"context"
	"io/fs"
	"net/http"
)

type Module interface {
	Name() string
	Migrations() fs.FS
	Routes(mux *http.ServeMux)
	Run(ctx context.Context) error
}
