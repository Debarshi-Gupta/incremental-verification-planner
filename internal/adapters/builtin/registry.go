// Package builtin wires the adapters shipped with IVP.
package builtin

import (
	"fmt"
	"strings"

	"github.com/ivp/ivp/internal/adapters"
	"github.com/ivp/ivp/internal/adapters/bazel"
	"github.com/ivp/ivp/internal/adapters/generic"
	"github.com/ivp/ivp/internal/adapters/gradle"
	"github.com/ivp/ivp/internal/adapters/jest"
	"github.com/ivp/ivp/internal/adapters/maven"
	"github.com/ivp/ivp/internal/adapters/nx"
	"github.com/ivp/ivp/internal/adapters/pytest"
)

func All() []adapters.Adapter {
	return []adapters.Adapter{gradle.New(), maven.New(), bazel.New(), nx.New(), pytest.New(), jest.New(), generic.New()}
}
func ByID(id string) (adapters.Adapter, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, a := range All() {
		if a.ID() == id {
			return a, nil
		}
	}
	return nil, fmt.Errorf("unsupported adapter %q", id)
}
