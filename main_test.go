package main

import (
	"strings"
	"testing"
)

// -network yardım metni mainnet'in durumunu izler: lansman commit'i yalnızca veri getirdiğinden
// metin kaynakta sabit olamaz.
func TestNetworkHelp(t *testing.T) {
	before, after := networkHelp(false), networkHelp(true)
	if !strings.Contains(before, "lansmanda") || strings.Contains(before, "mainnet, ") {
		t.Fatalf("lansman öncesi metin: %q", before)
	}
	if !strings.Contains(after, "mainnet, testnet, regtest") || strings.Contains(after, "lansmanda") {
		t.Fatalf("lansman sonrası metin: %q", after)
	}
}
