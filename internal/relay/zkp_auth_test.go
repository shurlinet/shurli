package relay

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestEncodeZKPAuthRequest(t *testing.T) {
	req := EncodeZKPAuthRequest(zkpAuthMembership, 0)
	if len(req) != 3 {
		t.Fatalf("expected 3 bytes, got %d", len(req))
	}
	if req[0] != zkpWireVersion {
		t.Fatalf("wrong version: %d", req[0])
	}
	if req[1] != zkpAuthMembership {
		t.Fatalf("wrong auth type: %d", req[1])
	}
	if req[2] != 0 {
		t.Fatalf("wrong role: %d", req[2])
	}
}

func TestEncodeZKPAuthRequest_Role(t *testing.T) {
	req := EncodeZKPAuthRequest(zkpAuthRole, 1) // admin
	if req[1] != zkpAuthRole {
		t.Fatalf("wrong auth type: %d", req[1])
	}
	if req[2] != 1 {
		t.Fatalf("wrong role: %d", req[2])
	}
}

func TestReadZKPChallenge(t *testing.T) {
	// Build a challenge message: [1 status=OK] [8 nonce] [32 root] [1 depth]
	var buf bytes.Buffer
	buf.WriteByte(zkpStatusOK)

	var nonceBuf [8]byte
	binary.BigEndian.PutUint64(nonceBuf[:], 12345)
	buf.Write(nonceBuf[:])

	root := make([]byte, 32)
	for i := range root {
		root[i] = byte(i)
	}
	buf.Write(root)
	buf.WriteByte(3) // depth

	nonce, gotRoot, depth, err := ReadZKPChallenge(&buf)
	if err != nil {
		t.Fatalf("ReadZKPChallenge: %v", err)
	}
	if nonce != 12345 {
		t.Fatalf("wrong nonce: %d", nonce)
	}
	if !bytes.Equal(gotRoot, root) {
		t.Fatalf("wrong root")
	}
	if depth != 3 {
		t.Fatalf("wrong depth: %d", depth)
	}
}

func TestReadZKPChallenge_Error(t *testing.T) {
	// Status = error, followed by a message.
	var buf bytes.Buffer
	buf.WriteByte(zkpStatusErr)
	buf.WriteByte(11) // msg len
	buf.WriteString("tree not ok")

	_, _, _, err := ReadZKPChallenge(&buf)
	if err == nil {
		t.Fatal("expected error for status=ERR")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("tree not ok")) {
		t.Fatalf("expected error message, got: %v", err)
	}
}

func TestEncodeZKPProof(t *testing.T) {
	proof := []byte("fake-proof-data-here")
	encoded := EncodeZKPProof(proof)

	if len(encoded) != 2+len(proof) {
		t.Fatalf("wrong encoded length: %d", len(encoded))
	}

	proofLen := binary.BigEndian.Uint16(encoded[0:2])
	if int(proofLen) != len(proof) {
		t.Fatalf("wrong proof length prefix: %d", proofLen)
	}
	if !bytes.Equal(encoded[2:], proof) {
		t.Fatal("proof payload mismatch")
	}
}

func TestReadZKPAuthResponse_Success(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteByte(zkpStatusOK)
	msg := "authorized"
	buf.WriteByte(byte(len(msg)))
	buf.WriteString(msg)

	ok, gotMsg, err := ReadZKPAuthResponse(&buf)
	if err != nil {
		t.Fatalf("ReadZKPAuthResponse: %v", err)
	}
	if !ok {
		t.Fatal("expected authorized=true")
	}
	if gotMsg != msg {
		t.Fatalf("wrong message: %q", gotMsg)
	}
}

func TestReadZKPAuthResponse_Rejected(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteByte(zkpStatusErr)
	msg := "proof verification failed"
	buf.WriteByte(byte(len(msg)))
	buf.WriteString(msg)

	ok, gotMsg, err := ReadZKPAuthResponse(&buf)
	if err != nil {
		t.Fatalf("ReadZKPAuthResponse: %v", err)
	}
	if ok {
		t.Fatal("expected authorized=false")
	}
	if gotMsg != msg {
		t.Fatalf("wrong message: %q", gotMsg)
	}
}

func TestReadZKPAuthResponse_EmptyMessage(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteByte(zkpStatusOK)
	buf.WriteByte(0) // no message

	ok, msg, err := ReadZKPAuthResponse(&buf)
	if err != nil {
		t.Fatalf("ReadZKPAuthResponse: %v", err)
	}
	if !ok {
		t.Fatal("expected authorized=true")
	}
	if msg != "" {
		t.Fatalf("expected empty message, got %q", msg)
	}
}
