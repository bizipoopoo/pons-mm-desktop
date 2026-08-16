package pons

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// TestLiveReadV1Launch_Bindings is an opt-in, read-only validation of the new
// launch bindings against the real pons v1 factory. It signs and spends nothing:
// every call is an eth_call. Crucially, predictTokenAddress takes the SAME
// TokenParams tuple + salt as launchToken, so a successful call proves the
// launch calldata encoding is correct end-to-end.
//
//	PUMPWATCH_LIVE=1 go test ./internal/pons -run TestLiveReadV1Launch_Bindings -v
//	# optionally: PONS_V1_TOKEN=0x... to also check GraduationStatus on a token
func TestLiveReadV1Launch_Bindings(t *testing.T) {
	if os.Getenv("PUMPWATCH_LIVE") == "" {
		t.Skip("set PUMPWATCH_LIVE=1 to run the live launch-binding test")
	}
	rpcURL := os.Getenv("PONS_RPC")
	if rpcURL == "" {
		rpcURL = DefaultRPC
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c, err := Dial(ctx, rpcURL)
	if err != nil {
		t.Fatal(err)
	}

	fee, err := c.LaunchFee(ctx)
	if err != nil {
		t.Fatalf("LaunchFee: %v", err)
	}
	if fee.Sign() < 0 {
		t.Fatalf("negative launch fee %s", fee)
	}
	t.Logf("launchFee = %s ETH", weiToEth(fee))

	cfg, err := c.GetLaunchConfig(ctx, 0)
	if err != nil {
		t.Fatalf("GetLaunchConfig(0): %v", err)
	}
	if cfg.Supply == nil || cfg.Supply.Sign() <= 0 {
		t.Fatalf("config 0 supply is non-positive: %v", cfg.Supply)
	}
	if cfg.GraduationThreshold == nil || cfg.GraduationThreshold.Sign() <= 0 {
		t.Fatalf("config 0 graduation threshold non-positive: %v", cfg.GraduationThreshold)
	}
	t.Logf("config 0: pair=%s supply=%s graduation=%s ETH maxWalletBps=%d maxTxBps=%d restrictionBlocks=%d enabled=%v",
		cfg.PairToken.Hex(), cfg.Supply, weiToEth(cfg.GraduationThreshold),
		cfg.MaxWalletBps, cfg.MaxTxBps, cfg.RestrictionBlocks, cfg.Enabled)
	if cfg.PairToken != common.HexToAddress(V1WETH) {
		t.Fatalf("config 0 pair token %s is not WETH %s", cfg.PairToken.Hex(), V1WETH)
	}

	// A throwaway deployer address; no funds needed for a view call.
	deployer := common.HexToAddress("0x000000000000000000000000000000000000dEaD")
	can, err := c.CanLaunch(ctx, deployer)
	if err != nil {
		t.Fatalf("CanLaunch: %v", err)
	}
	t.Logf("CanLaunch(%s) = %v", deployer.Hex(), can)

	// The real proof: encode the exact launchToken params tuple and have the
	// factory CREATE2-predict the token address. If the tuple/salt encoding
	// were wrong, this eth_call would revert or fail to decode.
	params := V1TokenParams{
		Name:        "ponsmm encoding probe",
		Symbol:      "PROBE",
		Logo:        "",
		Description: "read-only launch-encoding validation",
		Socials:     V1Socials{Twitter: "t", Telegram: "tg", Website: "https://example.com"},
		FeeWallet:   common.Address{},
	}
	var salt [32]byte
	predicted, err := c.PredictV1Token(ctx, params, 0, 0, salt, deployer)
	if err != nil {
		t.Fatalf("PredictV1Token (validates launchToken tuple encoding): %v", err)
	}
	if predicted == (common.Address{}) {
		t.Fatal("predicted token address is zero")
	}
	t.Logf("predictTokenAddress -> %s (launch calldata encoding OK)", predicted.Hex())

	if token := os.Getenv("PONS_V1_TOKEN"); token != "" {
		principal, threshold, graduated, err := c.GraduationStatus(ctx, common.HexToAddress(token))
		if err != nil {
			t.Fatalf("GraduationStatus: %v", err)
		}
		t.Logf("graduationStatus(%s): principal=%s ETH threshold=%s ETH graduated=%v",
			token, weiToEth(principal), weiToEth(threshold), graduated)
	}
}
