//go:build cgo && (darwin || linux)

package p2pnet

/*
// macOS: dns_sd functions are in libSystem (linked automatically).
// Linux: needs libavahi-compat-libdnssd (apt: libavahi-compat-libdnssd-dev).
#cgo linux LDFLAGS: -ldns_sd

#include <dns_sd.h>
#include <poll.h>
#include <stdlib.h>
#include <string.h>

// --- Browse ---

#define DNSSD_MAX_ENTRIES 64

typedef struct {
	char name[256];
	char regtype[256];
	char domain[256];
	uint32_t ifIndex;
	int isAdd;
} dnssd_browse_entry;

typedef struct {
	dnssd_browse_entry entries[DNSSD_MAX_ENTRIES];
	int count;
} dnssd_browse_ctx;

static void dnssd_browse_cb(
	DNSServiceRef sdRef, DNSServiceFlags flags, uint32_t ifIndex,
	DNSServiceErrorType errCode, const char *name,
	const char *regtype, const char *domain, void *ctx
) {
	if (errCode != kDNSServiceErr_NoError) return;
	dnssd_browse_ctx *bc = (dnssd_browse_ctx *)ctx;
	if (bc->count >= DNSSD_MAX_ENTRIES) return;
	dnssd_browse_entry *e = &bc->entries[bc->count++];
	strncpy(e->name, name, sizeof(e->name) - 1);
	e->name[sizeof(e->name) - 1] = '\0';
	strncpy(e->regtype, regtype, sizeof(e->regtype) - 1);
	e->regtype[sizeof(e->regtype) - 1] = '\0';
	strncpy(e->domain, domain, sizeof(e->domain) - 1);
	e->domain[sizeof(e->domain) - 1] = '\0';
	e->ifIndex = ifIndex;
	e->isAdd = (flags & kDNSServiceFlagsAdd) ? 1 : 0;
}

static DNSServiceErrorType dnssd_browse(DNSServiceRef *ref,
	const char *regtype, const char *domain, dnssd_browse_ctx *ctx) {
	return DNSServiceBrowse(ref, 0, 0, regtype, domain, dnssd_browse_cb, ctx);
}

// --- Resolve ---

typedef struct {
	unsigned char txtRecord[8192];
	uint16_t txtLen;
	int resolved;
} dnssd_resolve_ctx;

static void dnssd_resolve_cb(
	DNSServiceRef sdRef, DNSServiceFlags flags, uint32_t ifIndex,
	DNSServiceErrorType errCode, const char *fullname,
	const char *hosttarget, uint16_t port, uint16_t txtLen,
	const unsigned char *txtRecord, void *ctx
) {
	if (errCode != kDNSServiceErr_NoError) return;
	dnssd_resolve_ctx *rc = (dnssd_resolve_ctx *)ctx;
	if (txtLen > sizeof(rc->txtRecord)) txtLen = sizeof(rc->txtRecord);
	memcpy(rc->txtRecord, txtRecord, txtLen);
	rc->txtLen = txtLen;
	rc->resolved = 1;
}

static DNSServiceErrorType dnssd_resolve(DNSServiceRef *ref, uint32_t ifIndex,
	const char *name, const char *regtype, const char *domain,
	dnssd_resolve_ctx *ctx) {
	return DNSServiceResolve(ref, 0, ifIndex, name, regtype, domain,
		dnssd_resolve_cb, ctx);
}

// --- Helpers ---

// Poll fd for readability. Returns >0 readable, 0 timeout, <0 error.
static int dnssd_poll(int fd, int timeout_ms) {
	struct pollfd pfd;
	pfd.fd = fd;
	pfd.events = POLLIN;
	pfd.revents = 0;
	return poll(&pfd, 1, timeout_ms);
}
*/
import "C"

import (
	"context"
	"fmt"
	"log/slog"
	"time"
	"unsafe"
)

// nativeBrowse uses the platform's DNS-SD API (mDNSResponder on macOS,
// avahi-compat on Linux) to discover mDNS services. Cooperates with the
// system daemon via IPC rather than competing for multicast sockets.
// Sends discovered TXT record sets to entries. Blocks until ctx is done.
func nativeBrowse(ctx context.Context, service, domain string, entries chan<- []string) error {
	slog.Debug("mdns: native browse via dns_sd.h", "service", service, "domain", domain)

	cService := C.CString(service)
	defer C.free(unsafe.Pointer(cService))
	cDomain := C.CString(domain)
	defer C.free(unsafe.Pointer(cDomain))

	// Allocate browse context in C memory (safe from Go GC movement).
	bc := (*C.dnssd_browse_ctx)(C.calloc(1, C.sizeof_dnssd_browse_ctx))
	if bc == nil {
		return fmt.Errorf("dnssd: alloc failed")
	}
	defer C.free(unsafe.Pointer(bc))

	var ref C.DNSServiceRef
	errCode := C.dnssd_browse(&ref, cService, cDomain, bc)
	if errCode != C.kDNSServiceErr_NoError {
		return fmt.Errorf("dnssd: DNSServiceBrowse error %d", errCode)
	}
	defer C.DNSServiceRefDeallocate(ref)

	fd := C.DNSServiceRefSockFD(ref)
	if fd < 0 {
		return fmt.Errorf("dnssd: invalid socket fd")
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		// Poll with 500ms timeout so we can check ctx regularly.
		ret := C.dnssd_poll(C.int(fd), 500)
		if ret < 0 {
			return fmt.Errorf("dnssd: poll error")
		}
		if ret == 0 {
			continue // timeout, check ctx again
		}

		// Data available. Reset counter and process callbacks.
		bc.count = 0
		errCode = C.DNSServiceProcessResult(ref)
		if errCode != C.kDNSServiceErr_NoError {
			return fmt.Errorf("dnssd: ProcessResult error %d", errCode)
		}

		// Process each discovered service.
		for i := 0; i < int(bc.count); i++ {
			e := bc.entries[i]
			if e.isAdd == 0 {
				continue // removal event, skip
			}

			name := C.GoString(&e.name[0])
			regtype := C.GoString(&e.regtype[0])
			eDomain := C.GoString(&e.domain[0])

			txts := dnssdResolve(ctx, e.ifIndex, name, regtype, eDomain)
			if len(txts) > 0 {
				select {
				case entries <- txts:
				case <-ctx.Done():
					return nil
				}
			}
		}
	}
}

// dnssdResolve resolves a single mDNS service to get its TXT records.
// Returns nil on error or timeout (logged at debug level).
func dnssdResolve(ctx context.Context, ifIndex C.uint32_t, name, regtype, domain string) []string {
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	cRegtype := C.CString(regtype)
	defer C.free(unsafe.Pointer(cRegtype))
	cDomain := C.CString(domain)
	defer C.free(unsafe.Pointer(cDomain))

	rc := (*C.dnssd_resolve_ctx)(C.calloc(1, C.sizeof_dnssd_resolve_ctx))
	if rc == nil {
		return nil
	}
	defer C.free(unsafe.Pointer(rc))

	var ref C.DNSServiceRef
	errCode := C.dnssd_resolve(&ref, ifIndex, cName, cRegtype, cDomain, rc)
	if errCode != C.kDNSServiceErr_NoError {
		slog.Debug("dnssd: resolve start error", "error", int(errCode), "name", name)
		return nil
	}
	defer C.DNSServiceRefDeallocate(ref)

	fd := C.DNSServiceRefSockFD(ref)
	if fd < 0 {
		return nil
	}

	// Wait up to 5 seconds for resolve to complete (generous for LAN).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		ret := C.dnssd_poll(C.int(fd), 500)
		if ret < 0 {
			return nil
		}
		if ret == 0 {
			continue // timeout, try again
		}

		errCode = C.DNSServiceProcessResult(ref)
		if errCode != C.kDNSServiceErr_NoError {
			return nil
		}

		if rc.resolved != 0 {
			return parseDNSTXTWire(
				C.GoBytes(unsafe.Pointer(&rc.txtRecord[0]), C.int(rc.txtLen)),
			)
		}
	}

	slog.Debug("dnssd: resolve timeout", "name", name)
	return nil
}

// parseDNSTXTWire parses DNS TXT wire format into individual strings.
// Wire format: [length_byte][data_bytes...] repeated for each record.
func parseDNSTXTWire(data []byte) []string {
	var records []string
	i := 0
	for i < len(data) {
		l := int(data[i])
		i++
		if l == 0 || i+l > len(data) {
			break
		}
		records = append(records, string(data[i:i+l]))
		i += l
	}
	return records
}
