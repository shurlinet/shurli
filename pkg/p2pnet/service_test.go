package p2pnet

import (
	"errors"
	"testing"

	"github.com/libp2p/go-libp2p"
)

func newTestHost(t *testing.T) *ServiceRegistry {
	t.Helper()
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	t.Cleanup(func() { h.Close() })
	return NewServiceRegistry(h, nil)
}

func TestNewServiceRegistry(t *testing.T) {
	reg := newTestHost(t)
	if reg == nil {
		t.Fatal("NewServiceRegistry returned nil")
	}
	if len(reg.ListServices()) != 0 {
		t.Error("new registry should have no services")
	}
}

func TestRegisterService(t *testing.T) {
	reg := newTestHost(t)

	t.Run("valid", func(t *testing.T) {
		svc := &Service{
			Name:         "ssh",
			Protocol:     "/peerup/ssh/1.0.0",
			LocalAddress: "localhost:22",
			Enabled:      true,
		}
		if err := reg.RegisterService(svc); err != nil {
			t.Fatalf("RegisterService: %v", err)
		}
		services := reg.ListServices()
		if len(services) != 1 {
			t.Fatalf("got %d services, want 1", len(services))
		}
		if services[0].Name != "ssh" {
			t.Errorf("Name = %q, want %q", services[0].Name, "ssh")
		}
	})

	t.Run("nil service", func(t *testing.T) {
		if err := reg.RegisterService(nil); err == nil {
			t.Error("expected error for nil service")
		}
	})

	t.Run("empty name", func(t *testing.T) {
		svc := &Service{
			Name:         "",
			Protocol:     "/peerup/test/1.0.0",
			LocalAddress: "localhost:8080",
		}
		if err := reg.RegisterService(svc); err == nil {
			t.Error("expected error for empty name")
		}
	})

	t.Run("empty address", func(t *testing.T) {
		svc := &Service{
			Name:         "test",
			Protocol:     "/peerup/test/1.0.0",
			LocalAddress: "",
		}
		if err := reg.RegisterService(svc); err == nil {
			t.Error("expected error for empty address")
		}
	})

	t.Run("duplicate", func(t *testing.T) {
		svc := &Service{
			Name:         "ssh",
			Protocol:     "/peerup/ssh2/1.0.0",
			LocalAddress: "localhost:2222",
		}
		err := reg.RegisterService(svc)
		if err == nil {
			t.Error("expected error for duplicate name")
		}
		if !errors.Is(err, ErrServiceAlreadyRegistered) {
			t.Errorf("expected ErrServiceAlreadyRegistered, got: %v", err)
		}
	})
}

func TestUnregisterService(t *testing.T) {
	reg := newTestHost(t)

	svc := &Service{
		Name:         "ssh",
		Protocol:     "/peerup/ssh/1.0.0",
		LocalAddress: "localhost:22",
		Enabled:      true,
	}
	reg.RegisterService(svc)

	t.Run("exists", func(t *testing.T) {
		if err := reg.UnregisterService("ssh"); err != nil {
			t.Fatalf("UnregisterService: %v", err)
		}
		if len(reg.ListServices()) != 0 {
			t.Error("service should be removed")
		}
	})

	t.Run("not found", func(t *testing.T) {
		err := reg.UnregisterService("nonexistent")
		if err == nil {
			t.Error("expected error for nonexistent service")
		}
		if !errors.Is(err, ErrServiceNotFound) {
			t.Errorf("expected ErrServiceNotFound, got: %v", err)
		}
	})
}

func TestGetService(t *testing.T) {
	reg := newTestHost(t)

	svc := &Service{
		Name:         "ssh",
		Protocol:     "/peerup/ssh/1.0.0",
		LocalAddress: "localhost:22",
		Enabled:      true,
	}
	reg.RegisterService(svc)

	t.Run("found", func(t *testing.T) {
		got, ok := reg.GetService("ssh")
		if !ok {
			t.Fatal("expected service to be found")
		}
		if got.Name != "ssh" {
			t.Errorf("Name = %q", got.Name)
		}
		if got.LocalAddress != "localhost:22" {
			t.Errorf("LocalAddress = %q", got.LocalAddress)
		}
	})

	t.Run("not found", func(t *testing.T) {
		_, ok := reg.GetService("nonexistent")
		if ok {
			t.Error("expected service not to be found")
		}
	})
}

func TestListServices(t *testing.T) {
	reg := newTestHost(t)

	// Empty
	if len(reg.ListServices()) != 0 {
		t.Error("new registry should return empty list")
	}

	// Add two services
	reg.RegisterService(&Service{
		Name:         "ssh",
		Protocol:     "/peerup/ssh/1.0.0",
		LocalAddress: "localhost:22",
		Enabled:      true,
	})
	reg.RegisterService(&Service{
		Name:         "xrdp",
		Protocol:     "/peerup/xrdp/1.0.0",
		LocalAddress: "localhost:3389",
		Enabled:      true,
	})

	services := reg.ListServices()
	if len(services) != 2 {
		t.Fatalf("got %d services, want 2", len(services))
	}

	// Verify both are present (order not guaranteed from map)
	names := make(map[string]bool)
	for _, s := range services {
		names[s.Name] = true
	}
	if !names["ssh"] || !names["xrdp"] {
		t.Errorf("missing services: %v", names)
	}
}

func TestValidateServiceName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid lowercase", "ssh", false},
		{"valid with dash", "my-service", false},
		{"valid with numbers", "svc123", false},
		{"invalid slash", "foo/bar", true},
		{"invalid newline", "foo\nbar", true},
		{"invalid uppercase", "SSH", true},
		{"empty", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateServiceName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateServiceName(%q) error = %v, wantErr = %v", tt.input, err, tt.wantErr)
			}
		})
	}
}
