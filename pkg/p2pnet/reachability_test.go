package p2pnet

import "testing"

func TestReachabilityGradeA_IPv6(t *testing.T) {
	ifaces := &InterfaceSummary{HasGlobalIPv6: true}
	grade := ComputeReachabilityGrade(ifaces, nil)
	if grade.Grade != GradeA {
		t.Errorf("got %s, want A", grade.Grade)
	}
}

func TestReachabilityGradeB_IPv4(t *testing.T) {
	ifaces := &InterfaceSummary{HasGlobalIPv4: true}
	grade := ComputeReachabilityGrade(ifaces, nil)
	if grade.Grade != GradeB {
		t.Errorf("got %s, want B", grade.Grade)
	}
}

func TestReachabilityGradeA_IPv6_TakesPrecedence(t *testing.T) {
	ifaces := &InterfaceSummary{HasGlobalIPv6: true, HasGlobalIPv4: true}
	grade := ComputeReachabilityGrade(ifaces, nil)
	if grade.Grade != GradeA {
		t.Errorf("IPv6 should take precedence, got %s", grade.Grade)
	}
}

func TestReachabilityGradeB_FullConeNAT(t *testing.T) {
	ifaces := &InterfaceSummary{
		Interfaces: []InterfaceInfo{{Name: "en0"}},
	}
	stun := &STUNResult{NATType: NATFullCone}
	grade := ComputeReachabilityGrade(ifaces, stun)
	if grade.Grade != GradeB {
		t.Errorf("full-cone NAT should be B, got %s", grade.Grade)
	}
}

func TestReachabilityGradeB_AddressRestrictedNAT(t *testing.T) {
	ifaces := &InterfaceSummary{
		Interfaces: []InterfaceInfo{{Name: "en0"}},
	}
	stun := &STUNResult{NATType: NATAddressRestricted}
	grade := ComputeReachabilityGrade(ifaces, stun)
	if grade.Grade != GradeB {
		t.Errorf("address-restricted NAT should be B, got %s", grade.Grade)
	}
}

func TestReachabilityGradeC_PortRestrictedNAT(t *testing.T) {
	ifaces := &InterfaceSummary{
		Interfaces: []InterfaceInfo{{Name: "en0"}},
	}
	stun := &STUNResult{NATType: NATPortRestricted}
	grade := ComputeReachabilityGrade(ifaces, stun)
	if grade.Grade != GradeC {
		t.Errorf("port-restricted NAT should be C, got %s", grade.Grade)
	}
}

func TestReachabilityGradeD_SymmetricNAT(t *testing.T) {
	ifaces := &InterfaceSummary{
		Interfaces: []InterfaceInfo{{Name: "en0"}},
	}
	stun := &STUNResult{NATType: NATSymmetric}
	grade := ComputeReachabilityGrade(ifaces, stun)
	if grade.Grade != GradeD {
		t.Errorf("symmetric NAT should be D, got %s", grade.Grade)
	}
}

func TestReachabilityGradeD_NoPublicAddr(t *testing.T) {
	ifaces := &InterfaceSummary{
		Interfaces: []InterfaceInfo{{Name: "en0"}},
	}
	grade := ComputeReachabilityGrade(ifaces, nil)
	if grade.Grade != GradeD {
		t.Errorf("no public addr should be D, got %s", grade.Grade)
	}
}

func TestReachabilityGradeF_Offline(t *testing.T) {
	grade := ComputeReachabilityGrade(nil, nil)
	if grade.Grade != GradeF {
		t.Errorf("nil everything should be F, got %s", grade.Grade)
	}
}

func TestReachabilityGradeF_EmptyInterfaces(t *testing.T) {
	ifaces := &InterfaceSummary{}
	grade := ComputeReachabilityGrade(ifaces, nil)
	if grade.Grade != GradeF {
		t.Errorf("empty interfaces should be F, got %s", grade.Grade)
	}
}

func TestReachabilityGradeC_UnknownNATWithExternalAddr(t *testing.T) {
	ifaces := &InterfaceSummary{
		Interfaces: []InterfaceInfo{{Name: "en0"}},
	}
	stun := &STUNResult{
		NATType:       NATUnknown,
		ExternalAddrs: []string{"203.0.113.50:12345"},
	}
	grade := ComputeReachabilityGrade(ifaces, stun)
	if grade.Grade != GradeC {
		t.Errorf("unknown NAT with external addr should be C, got %s", grade.Grade)
	}
}

func TestReachabilityGradeB_NATNoneFromSTUN(t *testing.T) {
	ifaces := &InterfaceSummary{
		Interfaces: []InterfaceInfo{{Name: "en0"}},
	}
	stun := &STUNResult{NATType: NATNone}
	grade := ComputeReachabilityGrade(ifaces, stun)
	if grade.Grade != GradeB {
		t.Errorf("STUN no-NAT should be B, got %s", grade.Grade)
	}
}

func TestReachabilityGradeD_CGNAT_OverridesPortRestricted(t *testing.T) {
	ifaces := &InterfaceSummary{
		Interfaces: []InterfaceInfo{{Name: "pdp_ip0"}},
	}
	stun := &STUNResult{
		NATType:     NATPortRestricted,
		BehindCGNAT: true,
		CGNATNote:   "RFC 6598 CGNAT address detected on pdp_ip0 (100.64.1.5)",
	}
	grade := ComputeReachabilityGrade(ifaces, stun)
	if grade.Grade != GradeD {
		t.Errorf("CGNAT behind port-restricted NAT should be D, got %s", grade.Grade)
	}
	if grade.Description != "CGNAT detected, hole-punch unlikely" {
		t.Errorf("wrong description: %s", grade.Description)
	}
}

func TestReachabilityGradeD_CGNAT_OverridesFullCone(t *testing.T) {
	ifaces := &InterfaceSummary{
		Interfaces: []InterfaceInfo{{Name: "wwan0"}},
	}
	stun := &STUNResult{
		NATType:     NATFullCone,
		BehindCGNAT: true,
	}
	grade := ComputeReachabilityGrade(ifaces, stun)
	if grade.Grade != GradeD {
		t.Errorf("CGNAT should cap at D even with full-cone inner NAT, got %s", grade.Grade)
	}
}
