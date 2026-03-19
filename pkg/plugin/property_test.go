package plugin

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// TestProperty_RegistryStateMachine runs random sequences of Register/Enable/
// Disable/Query operations and verifies the model matches the implementation
// at every step.
func TestProperty_RegistryStateMachine(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		r := NewRegistry(&ContextProvider{})
		r.enableDisableCooldown = 0

		// Model: simple map tracking expected state per plugin.
		type modelEntry struct {
			state    State
			commands int
		}
		model := make(map[string]*modelEntry)
		registered := []string{}

		// Generate 20-50 random operations.
		numOps := rapid.IntRange(20, 50).Draw(t, "numOps")

		for i := 0; i < numOps; i++ {
			op := rapid.IntRange(0, 3).Draw(t, fmt.Sprintf("op-%d", i))

			switch op {
			case 0: // Register
				name := fmt.Sprintf("p%d", len(registered))
				if len(registered) >= 10 {
					continue // cap at 10 plugins
				}
				numCmds := rapid.IntRange(0, 3).Draw(t, fmt.Sprintf("cmds-%d", i))
				cmds := make([]Command, numCmds)
				for j := range cmds {
					cmds[j] = Command{Name: fmt.Sprintf("%s-cmd-%d", name, j)}
				}
				p := newMinimalPlugin(name)
				p.commands = cmds

				if err := r.Register(p); err != nil {
					t.Fatalf("Register %s: %v", name, err)
				}
				model[name] = &modelEntry{state: StateReady, commands: numCmds}
				registered = append(registered, name)

			case 1: // Enable
				if len(registered) == 0 {
					continue
				}
				idx := rapid.IntRange(0, len(registered)-1).Draw(t, fmt.Sprintf("enable-idx-%d", i))
				name := registered[idx]
				m := model[name]

				err := r.Enable(name)
				if m.state == StateActive {
					// Idempotent.
					if err != nil {
						t.Errorf("Enable idempotent should not error: %v", err)
					}
				} else if m.state == StateReady || m.state == StateStopped {
					if err != nil {
						t.Errorf("Enable from %s should succeed: %v", m.state, err)
					} else {
						m.state = StateActive
					}
				}

			case 2: // Disable
				if len(registered) == 0 {
					continue
				}
				idx := rapid.IntRange(0, len(registered)-1).Draw(t, fmt.Sprintf("disable-idx-%d", i))
				name := registered[idx]
				m := model[name]

				r.Disable(name)
				if m.state == StateActive || m.state == StateReady {
					m.state = StateStopped
				}

			case 3: // Query AllCommands
				cmds := r.AllCommands()
				expectedCount := 0
				for _, m := range model {
					if m.state == StateActive {
						expectedCount += m.commands
					}
				}
				if len(cmds) != expectedCount {
					t.Errorf("AllCommands: expected %d, got %d", expectedCount, len(cmds))
				}
			}

			// Invariant: AllCommands only returns from ACTIVE plugins.
			cmds := r.AllCommands()
			activeCommands := 0
			for _, m := range model {
				if m.state == StateActive {
					activeCommands += m.commands
				}
			}
			if len(cmds) != activeCommands {
				t.Errorf("invariant violation: AllCommands=%d, expected=%d", len(cmds), activeCommands)
			}

			// Invariant: no plugin in invalid state.
			for name, m := range model {
				info, err := r.GetInfo(name)
				if err != nil {
					t.Errorf("GetInfo(%s): %v", name, err)
					continue
				}
				if info.State != m.state {
					t.Errorf("state mismatch for %s: model=%s, actual=%s", name, m.state, info.State)
				}
			}
		}

		// Final invariant: DisableAll leaves zero ACTIVE.
		r.DisableAll()
		for name, m := range model {
			if m.state == StateActive {
				m.state = StateStopped
			}
			info, _ := r.GetInfo(name)
			if info.State == StateActive {
				t.Errorf("DisableAll: %s still ACTIVE", name)
			}
		}
	})
}

// TestProperty_CapabilityIsolation verifies that no method on PluginContext
// returns types from internal/* packages.
func TestProperty_CapabilityIsolation(t *testing.T) {
	ctxType := reflect.TypeOf(&PluginContext{})

	for i := 0; i < ctxType.NumMethod(); i++ {
		method := ctxType.Method(i)
		methodType := method.Type

		// Check all return types.
		for j := 0; j < methodType.NumOut(); j++ {
			outType := methodType.Out(j)
			pkgPath := outType.PkgPath()

			// Also check pointer/slice element types.
			elemType := outType
			for elemType.Kind() == reflect.Ptr || elemType.Kind() == reflect.Slice {
				elemType = elemType.Elem()
			}
			if elemType.PkgPath() != "" {
				pkgPath = elemType.PkgPath()
			}

			if strings.Contains(pkgPath, "/internal/") {
				t.Errorf("PluginContext.%s returns type %s from internal package %s",
					method.Name, outType, pkgPath)
			}
		}

		// Check all parameter types (excluding receiver).
		for j := 1; j < methodType.NumIn(); j++ {
			inType := methodType.In(j)
			elemType := inType
			for elemType.Kind() == reflect.Ptr || elemType.Kind() == reflect.Slice {
				elemType = elemType.Elem()
			}
			if strings.Contains(elemType.PkgPath(), "/internal/") {
				t.Errorf("PluginContext.%s accepts type %s from internal package %s",
					method.Name, inType, elemType.PkgPath())
			}
		}
	}
}
