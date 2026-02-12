#!/bin/bash
# Deploy relay server to a fresh Linode (Ubuntu 24.04)
# Run this script ON the Linode after SSH'ing in

set -e

echo "=== Setting up libp2p relay server ==="

# Install Go
echo "Installing Go..."
wget -q https://go.dev/dl/go1.22.5.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.22.5.linux-amd64.tar.gz
rm go1.22.5.linux-amd64.tar.gz
export PATH=$PATH:/usr/local/go/bin
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc

# Increase UDP buffer sizes (for QUIC)
echo "Tuning network buffers..."
sudo sysctl -w net.core.rmem_max=7500000
sudo sysctl -w net.core.wmem_max=7500000
echo "net.core.rmem_max=7500000" | sudo tee -a /etc/sysctl.conf
echo "net.core.wmem_max=7500000" | sudo tee -a /etc/sysctl.conf

# Create app directory
mkdir -p /opt/relay-server
cd /opt/relay-server

# Copy the relay server code here (or git clone your repo)
echo "Place your relay-server Go files in /opt/relay-server/"
echo "Then run:"
echo "  cd /opt/relay-server"
echo "  go mod tidy"
echo "  go build -o relay-server ."
echo ""
echo "To run as a service, install the systemd unit:"
echo "  sudo cp relay-server.service /etc/systemd/system/"
echo "  sudo systemctl daemon-reload"
echo "  sudo systemctl enable relay-server"
echo "  sudo systemctl start relay-server"
echo ""

# Open firewall ports
echo "Opening firewall ports..."
sudo ufw allow 4001/tcp
sudo ufw allow 4001/udp
echo "Firewall configured."

echo ""
echo "=== Setup complete ==="
echo "Next steps:"
echo "1. Copy relay-server code to /opt/relay-server/"
echo "2. go mod tidy && go build -o relay-server ."
echo "3. ./relay-server  (test it)"
echo "4. Copy the Peer ID and addresses into your home-node and client-node configs"
echo "5. Install systemd service for auto-start"
