package p2pnet

// ReachabilityGrade summarizes how reachable this node is from the internet.
type ReachabilityGrade struct {
	Grade       string `json:"grade"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// Grade constants ordered from best to worst.
const (
	GradeA = "A"
	GradeB = "B"
	GradeC = "C"
	GradeD = "D"
	GradeF = "F"
)

// ComputeReachabilityGrade determines the reachability grade from interface
// discovery and STUN probe results. Either parameter may be nil.
//
// Grade scale:
//
//	A  Excellent  Public IPv6 detected
//	B  Good       Public IPv4 or hole-punchable NAT (full-cone / address-restricted)
//	C  Fair       Port-restricted NAT
//	D  Poor       Symmetric NAT / CGNAT
//	F  Offline    No connectivity detected
func ComputeReachabilityGrade(ifaces *InterfaceSummary, stun *STUNResult) ReachabilityGrade {
	hasIPv6 := ifaces != nil && ifaces.HasGlobalIPv6
	hasIPv4 := ifaces != nil && ifaces.HasGlobalIPv4

	if hasIPv6 {
		return ReachabilityGrade{
			Grade:       GradeA,
			Label:       "Excellent",
			Description: "Public IPv6 detected",
		}
	}

	if hasIPv4 {
		return ReachabilityGrade{
			Grade:       GradeB,
			Label:       "Good",
			Description: "Public IPv4 detected",
		}
	}

	// No public IP directly visible. Check STUN results for NAT type.
	if stun != nil {
		// If CGNAT is confirmed, cap at grade D regardless of NAT type.
		// Hole-punching through the inner NAT is irrelevant when a carrier
		// NAT sits above it and drops unsolicited inbound packets.
		if stun.BehindCGNAT {
			return ReachabilityGrade{
				Grade:       GradeD,
				Label:       "Poor",
				Description: "CGNAT detected, hole-punch unlikely",
			}
		}

		switch stun.NATType {
		case NATNone:
			// STUN says no NAT but we didn't detect a public IP above.
			// Likely a misconfiguration, but the STUN external address works.
			return ReachabilityGrade{
				Grade:       GradeB,
				Label:       "Good",
				Description: "STUN reports no NAT",
			}
		case NATFullCone, NATAddressRestricted:
			return ReachabilityGrade{
				Grade:       GradeB,
				Label:       "Good",
				Description: "Hole-punchable NAT (" + string(stun.NATType) + ")",
			}
		case NATPortRestricted:
			return ReachabilityGrade{
				Grade:       GradeC,
				Label:       "Fair",
				Description: "Port-restricted NAT",
			}
		case NATSymmetric:
			return ReachabilityGrade{
				Grade:       GradeD,
				Label:       "Poor",
				Description: "Symmetric NAT (CGNAT likely)",
			}
		}

		// NATUnknown but STUN returned external addresses means some connectivity.
		if len(stun.ExternalAddrs) > 0 {
			return ReachabilityGrade{
				Grade:       GradeC,
				Label:       "Fair",
				Description: "NAT type unknown, external address discovered",
			}
		}
	}

	// No public IP and no useful STUN results.
	if ifaces != nil && len(ifaces.Interfaces) > 0 {
		// We have network interfaces but no public connectivity.
		return ReachabilityGrade{
			Grade:       GradeD,
			Label:       "Poor",
			Description: "No public address detected",
		}
	}

	return ReachabilityGrade{
		Grade:       GradeF,
		Label:       "Offline",
		Description: "No network connectivity detected",
	}
}
