package internal_test

import (
	"os/exec"
	"strings"
	"testing"
)

// Domain packages must not know about storage, transport, the desktop shell
// or the operating system; adapters depend on them, never the reverse.
var domainPackages = []string{"account", "identity", "usage", "quota", "attribution"}

var forbiddenImports = []string{
	"database/sql",
	"net/http",
	"os/exec",
	"github.com/wailsapp/",
	"github.com/jackc/pgx",
	"modernc.org/sqlite",
	"github.com/KoukeNeko/ShareCodex/internal/provider",
	"github.com/KoukeNeko/ShareCodex/internal/client",
	"github.com/KoukeNeko/ShareCodex/internal/server",
}

func TestDomainPackagesStayPure(t *testing.T) {
	for _, pkg := range domainPackages {
		out, err := exec.Command("go", "list", "-deps", "./"+pkg).CombinedOutput()
		if err != nil {
			t.Fatalf("go list %s: %v\n%s", pkg, err, out)
		}
		for _, dep := range strings.Fields(string(out)) {
			for _, bad := range forbiddenImports {
				if strings.HasPrefix(dep, bad) {
					t.Errorf("internal/%s depends on %s", pkg, dep)
				}
			}
		}
	}
}
