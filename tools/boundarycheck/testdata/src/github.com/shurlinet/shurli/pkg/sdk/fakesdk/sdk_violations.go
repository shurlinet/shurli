package fakesdk

// This test file verifies that boundarycheck flags plugin-specific code in pkg/sdk/.

// TransferService is a plugin engine type that should not be in pkg/sdk/.
type TransferService struct {
	dir string
}

func (ts *TransferService) SendFile() {} // want `plugin engine method TransferService.SendFile\(\) is defined in pkg/sdk/ but belongs in a plugin package`

// TransferProtocol is a plugin-specific protocol constant.
const TransferProtocol = "/shurli/file-transfer/2.0.0" // want `plugin-specific protocol constant TransferProtocol`

// Network is a generic SDK type - should NOT be flagged.
type Network struct{}

func (n *Network) Connect() {} // OK - generic SDK type
