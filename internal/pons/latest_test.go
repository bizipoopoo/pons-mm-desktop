package pons

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func packedLaunchCalldata(t *testing.T, params V2TokenParams) []byte {
	t.Helper()
	data, err := factoryABI.Pack("launchToken", params, big.NewInt(0),
		common.HexToAddress(NativeQuote), []common.Address{})
	if err != nil {
		t.Fatalf("pack launchToken: %v", err)
	}
	return data
}

func TestFillLaunchMetaFromDirectFactoryCall(t *testing.T) {
	params := V2TokenParams{
		Name: "Direct Token", Symbol: "DIR",
		Logo: "https://example.com/dir.png", Description: "direct launch",
		Socials: V2Socials{Twitter: "https://x.com/dir"},
	}
	var meta LaunchTokenMeta
	if !fillLaunchMetaFromCalldata(&meta, packedLaunchCalldata(t, params)) {
		t.Fatal("direct launchToken calldata was not decoded")
	}
	if meta.Name != params.Name || meta.Symbol != params.Symbol ||
		meta.Logo != params.Logo || meta.Description != params.Description ||
		meta.Socials.Twitter != params.Socials.Twitter {
		t.Fatalf("decoded meta mismatch: %+v", meta)
	}
}

// Launches through the official router use a wrapper selector unknown to us,
// with the same params tuple embedded at an offset referenced from the
// calldata head. The offset scan must still recover the metadata.
func TestFillLaunchMetaFromWrapperCall(t *testing.T) {
	params := V2TokenParams{
		Name: "Wrapped Token", Symbol: "WRAP",
		Logo: "ipfs://bafkexample", Description: "launched via router",
		Socials: V2Socials{Website: "https://wrap.example"},
	}
	data := packedLaunchCalldata(t, params)
	copy(data[:4], []byte{0xf8, 0x5f, 0x8e, 0x41}) // unknown wrapper selector

	var meta LaunchTokenMeta
	if !fillLaunchMetaFromCalldata(&meta, data) {
		t.Fatal("wrapper calldata was not decoded by the offset scan")
	}
	if meta.Name != params.Name || meta.Symbol != params.Symbol ||
		meta.Logo != params.Logo || meta.Description != params.Description ||
		meta.Socials.Website != params.Socials.Website {
		t.Fatalf("decoded meta mismatch: %+v", meta)
	}
}

func TestFillLaunchMetaRejectsGarbage(t *testing.T) {
	var meta LaunchTokenMeta
	garbage := make([]byte, 4+32*12)
	for i := range garbage {
		garbage[i] = byte(i * 31)
	}
	if fillLaunchMetaFromCalldata(&meta, garbage) {
		t.Fatalf("garbage calldata unexpectedly decoded: %+v", meta)
	}
	if fillLaunchMetaFromCalldata(&meta, nil) {
		t.Fatal("empty calldata unexpectedly decoded")
	}
}
